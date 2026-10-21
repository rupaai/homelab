//! Reads metadata (size, dates, camera, GPS, tags) from uploaded files.
//!
//! Files come from untrusted apps and servers, so this module only uses
//! memory-safe Rust parsers, never decodes pixels or audio, reads through a
//! capped reader, and treats every failure (parse error, panic, timeout) as
//! "nothing found". The extractor is chosen from the sniffed content type,
//! never from the client's file name or Content-Type.

use std::io::{self, BufReader, Read, Seek, SeekFrom};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::path::{Path, PathBuf};
use std::time::Duration;

use chrono::{DateTime, Datelike, NaiveDate, NaiveDateTime, SecondsFormat, Utc};
use nom_exif::{ExifDateTime, ExifTag, MediaParser, MediaSource, TrackInfoTag};
use serde_json::{Map, Value};

use crate::disk::open_file;

/// How long extraction may take before it is given up.
const TIMEOUT: Duration = Duration::from_secs(10);
/// A parser never reads more than this many bytes from a file.
const MAX_READ: u64 = 64 * 1024 * 1024;
/// PDFs are read whole by the parser, so big ones are skipped.
const MAX_PDF: u64 = 100 * 1024 * 1024;
/// The serialized metadata may not be bigger than this.
const MAX_META: usize = 4096;
/// Longest string value, in characters.
const MAX_STRING: usize = 200;
/// At most this many files are read at once. A file that runs past the
/// timeout keeps its slot until its parser really stops, so bad files can't
/// pile up threads.
const MAX_AT_ONCE: usize = 2;
/// Each parser runs on its own thread with a large stack, so deeply nested
/// input runs out of time or budget rather than overflowing the stack (which
/// would stop the whole program; catch_unwind can't stop that).
const STACK: usize = 64 * 1024 * 1024;

static SLOTS: tokio::sync::Semaphore = tokio::sync::Semaphore::const_new(MAX_AT_ONCE);

/// The metadata found for one file.
pub struct Found {
    /// A JSON object. Empty when nothing was found.
    pub meta: Value,
    /// `meta.taken_at` as stored for searching (see [`taken_at_key`]).
    pub taken_at: Option<String>,
}

impl Found {
    fn empty() -> Found {
        Found {
            meta: Value::Object(Map::new()),
            taken_at: None,
        }
    }
}

/// Extracts the metadata of the stored file at `path`, whose content type was
/// sniffed as `content_type`. Never fails: any problem gives empty metadata.
/// `id` is only used for the warning in the log.
pub async fn extract(path: PathBuf, content_type: String, id: &str) -> Found {
    let work = async {
        let Ok(slot) = SLOTS.acquire().await else {
            return Err("closed");
        };
        let (tx, rx) = tokio::sync::oneshot::channel();
        std::thread::Builder::new()
            .name("metadata".into())
            .stack_size(STACK)
            .spawn(move || {
                let found = catch_unwind(AssertUnwindSafe(|| extract_sync(&path, &content_type)));
                drop(slot); // the slot frees only when the parser has stopped
                let _ = tx.send(found);
            })
            .map_err(|_| "no thread")?;
        match rx.await {
            Ok(Ok(found)) => Ok(found),
            Ok(Err(_)) => Err("panicked"),
            Err(_) => Err("failed"),
        }
    };
    match tokio::time::timeout(TIMEOUT, work).await {
        Ok(Ok(found)) => found,
        Ok(Err(why)) => {
            tracing::warn!(%id, "metadata extraction {why}");
            Found::empty()
        }
        Err(_) => {
            tracing::warn!(%id, "metadata extraction timed out");
            Found::empty()
        }
    }
}

/// The blocking part of [`extract`]. Can panic if a parser does.
pub fn extract_sync(path: &Path, content_type: &str) -> Found {
    let mut m = Meta::default();
    let result = match content_type {
        "image/jpeg" | "image/png" | "image/heic" | "image/heif" | "image/webp" | "image/tiff"
        | "image/gif" => image(path, content_type, &mut m),
        "application/pdf" => pdf(path, &mut m),
        t if t.starts_with("video/") => video(path, &mut m),
        t if t.starts_with("audio/") => audio(path, &mut m),
        _ => Ok(()),
    };
    if result.is_err() && m.0.is_empty() {
        return Found::empty();
    }
    m.finish()
}

// ---- the result under construction ----

#[derive(Default)]
struct Meta(Map<String, Value>);

impl Meta {
    fn has(&self, key: &str) -> bool {
        self.0.contains_key(key)
    }

    /// Adds a text value: control characters dropped, at most 200 characters.
    fn text(&mut self, key: &str, value: &str) {
        if let Some(s) = clean_text(value) {
            self.0.insert(key.to_string(), Value::String(s));
        }
    }

    fn uint(&mut self, key: &str, value: u64) {
        self.0.insert(key.to_string(), Value::from(value));
    }

    /// Adds a number, unless it is NaN or infinite.
    fn float(&mut self, key: &str, value: f64) {
        if let Some(n) = serde_json::Number::from_f64(value).filter(|_| value.is_finite()) {
            self.0.insert(key.to_string(), Value::Number(n));
        }
    }

    fn width_height(&mut self, w: u64, h: u64) {
        // Some broken files say 0 or something absurd.
        if (1..=1 << 24).contains(&w) && (1..=1 << 24).contains(&h) {
            self.uint("width", w);
            self.uint("height", h);
        }
    }

    fn taken(&mut self, value: ExifDateTime, min_year: i32) {
        let text = match value {
            ExifDateTime::Aware(dt) => (dt.year() >= min_year && dt.year() <= 2100)
                .then(|| dt.to_rfc3339_opts(SecondsFormat::Secs, false)),
            ExifDateTime::Naive(dt) => (dt.year() >= min_year && dt.year() <= 2100)
                .then(|| dt.format("%Y-%m-%dT%H:%M:%S").to_string()),
        };
        if let Some(t) = text {
            self.0.insert("taken_at".to_string(), Value::String(t));
        }
    }

    fn gps(&mut self, lat: Option<f64>, lon: Option<f64>, alt: Option<f64>) {
        let (Some(lat), Some(lon)) = (lat, lon) else {
            return;
        };
        if !(lat.is_finite() && lon.is_finite()) || lat.abs() > 90.0 || lon.abs() > 180.0 {
            return;
        }
        let mut g = Map::new();
        g.insert("lat".into(), Value::from(lat));
        g.insert("lon".into(), Value::from(lon));
        if let Some(a) = alt.filter(|a| a.is_finite() && a.abs() < 100_000.0) {
            g.insert("alt".into(), Value::from(a));
        }
        self.0.insert("gps".to_string(), Value::Object(g));
    }

    /// Applies the size limit and works out the search key.
    fn finish(self) -> Found {
        let meta = Value::Object(self.0);
        if meta.to_string().len() > MAX_META {
            return Found::empty();
        }
        let taken_at = meta
            .get("taken_at")
            .and_then(Value::as_str)
            .and_then(taken_at_key);
        Found { meta, taken_at }
    }
}

/// Drops control characters, trims, and keeps at most 200 characters.
/// `None` if nothing is left.
pub fn clean_text(s: &str) -> Option<String> {
    let s: String = s
        .chars()
        .filter(|c| !c.is_control())
        .take(MAX_STRING)
        .collect();
    let s = s.trim();
    (!s.is_empty()).then(|| s.to_string())
}

// ---- dates ----

/// The value stored in the `taken_at` column for a `meta.taken_at` text:
/// with an offset, the same moment in UTC (`2023-07-09T12:36:33Z`); without
/// one, the text as given (`2023-07-09T20:36:33`).
pub fn taken_at_key(text: &str) -> Option<String> {
    if let Ok(dt) = DateTime::parse_from_rfc3339(text) {
        return Some(
            dt.with_timezone(&Utc)
                .format("%Y-%m-%dT%H:%M:%SZ")
                .to_string(),
        );
    }
    NaiveDateTime::parse_from_str(text, "%Y-%m-%dT%H:%M:%S")
        .ok()
        .map(|d| d.format("%Y-%m-%dT%H:%M:%S").to_string())
}

/// Reads a `taken_after` / `taken_before` value (an RFC 3339 date or
/// date-time) into the form that is compared with the `taken_at` column:
/// UTC, to the second, without the trailing `Z`. Compared as text, that
/// gives `taken_at >= bound` and `taken_at < bound` for stored values with
/// the `Z` as well as for stored values without an offset.
pub fn parse_taken_bound(text: &str) -> Option<String> {
    if let Ok(dt) = DateTime::parse_from_rfc3339(text) {
        return Some(
            dt.with_timezone(&Utc)
                .format("%Y-%m-%dT%H:%M:%S")
                .to_string(),
        );
    }
    let date = NaiveDate::parse_from_str(text, "%Y-%m-%d").ok()?;
    (text.len() == 10).then(|| format!("{}T00:00:00", date.format("%Y-%m-%d")))
}

// ---- reading ----

/// A reader that gives up (end of file) after `left` bytes have been read.
/// Seeking is free, so formats with their metadata at the end still work.
struct Capped<R> {
    inner: R,
    left: u64,
}

impl<R> Capped<R> {
    fn new(inner: R) -> Self {
        Capped {
            inner,
            left: MAX_READ,
        }
    }
}

impl<R: Read> Read for Capped<R> {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        let n = buf.len().min(self.left.min(usize::MAX as u64) as usize);
        if n == 0 {
            return Ok(0);
        }
        let got = self.inner.read(&mut buf[..n])?;
        self.left -= got as u64;
        Ok(got)
    }
}

impl<R: Seek> Seek for Capped<R> {
    fn seek(&mut self, pos: SeekFrom) -> io::Result<u64> {
        self.inner.seek(pos)
    }
}

fn open_capped(path: &Path) -> io::Result<Capped<std::fs::File>> {
    open_file(path).map(Capped::new)
}

fn failed(what: impl std::fmt::Display) -> io::Error {
    io::Error::other(what.to_string())
}

// ---- images ----

fn image(path: &Path, content_type: &str, m: &mut Meta) -> io::Result<()> {
    // Sizes come from the headers; nothing is decoded.
    if let Ok(size) = imagesize::reader_size(BufReader::new(open_capped(path)?)) {
        m.width_height(size.width as u64, size.height as u64);
    }
    if content_type == "image/gif" {
        return Ok(());
    }
    let mut parser = MediaParser::new();
    let source = MediaSource::seekable(open_capped(path)?).map_err(failed)?;
    let exif: nom_exif::Exif = parser.parse_exif(source).map_err(failed)?.into();

    let text = |tag| exif.get(tag).and_then(|v| v.as_str());
    if let Some(v) = text(ExifTag::Make) {
        m.text("camera_make", v);
    }
    if let Some(v) = text(ExifTag::Model) {
        m.text("camera_model", v);
    }
    if let Some(v) = text(ExifTag::LensModel) {
        m.text("lens", v);
    }
    if let Some(o) = exif
        .get(ExifTag::Orientation)
        .and_then(|v| v.try_as_integer())
        .filter(|o| (1..=8).contains(o))
    {
        m.uint("orientation", o as u64);
    }
    if let Some(t) = exif
        .get(ExifTag::DateTimeOriginal)
        .and_then(|v| v.as_datetime())
    {
        m.taken(t, 1900);
    }
    if !m.has("width") {
        let n = |tag| exif.get(tag).and_then(|v| v.try_as_integer());
        if let (Some(w), Some(h)) = (n(ExifTag::ExifImageWidth), n(ExifTag::ExifImageHeight)) {
            if w > 0 && h > 0 {
                m.width_height(w as u64, h as u64);
            }
        }
    }
    if let Some(g) = exif.gps_info() {
        m.gps(
            g.latitude_decimal(),
            g.longitude_decimal(),
            g.altitude_meters(),
        );
    }
    Ok(())
}

// ---- video ----

fn video(path: &Path, m: &mut Meta) -> io::Result<()> {
    let mut parser = MediaParser::new();
    let source = MediaSource::seekable(open_capped(path)?).map_err(failed)?;
    let info = match parser.parse_track(source) {
        Ok(info) => info,
        Err(e) => {
            // Audio-only MP4 files (M4A) can be sniffed as video.
            let _ = audio(path, m);
            return Err(failed(e));
        }
    };
    let n = |tag| info.get(tag).and_then(|v| v.try_as_integer());
    if let (Some(w), Some(h)) = (n(TrackInfoTag::Width), n(TrackInfoTag::Height)) {
        if w > 0 && h > 0 {
            m.width_height(w as u64, h as u64);
        }
    }
    if let Some(ms) = n(TrackInfoTag::DurationMs).filter(|ms| *ms > 0) {
        m.float("duration_s", ms as f64 / 1000.0);
    }
    if let Some(t) = info
        .get(TrackInfoTag::CreateDate)
        .and_then(|v| v.as_datetime())
    {
        // Containers use 0 (the year 1904) for "unknown".
        m.taken(t, 1970);
    }
    if let Some(g) = info.gps_info() {
        m.gps(
            g.latitude_decimal(),
            g.longitude_decimal(),
            g.altitude_meters(),
        );
    }
    if !m.has("width") {
        // No picture track: it may be an audio file in an MP4 wrapper.
        let _ = audio(path, m);
    }
    Ok(())
}

// ---- audio ----

fn audio(path: &Path, m: &mut Meta) -> io::Result<()> {
    use lofty::config::ParseOptions;
    use lofty::file::{AudioFile, TaggedFileExt};
    use lofty::probe::Probe;
    use lofty::tag::{Accessor, ItemKey};

    let options = ParseOptions::new().read_cover_art(false);
    let tagged = Probe::new(BufReader::new(open_capped(path)?))
        .options(options)
        .guess_file_type()?
        .read()
        .map_err(failed)?;

    let secs = tagged.properties().duration().as_secs_f64();
    if secs > 0.0 {
        m.float("duration_s", secs);
    }
    let Some(tag) = tagged.primary_tag().or_else(|| tagged.first_tag()) else {
        return Ok(());
    };
    if let Some(v) = tag.title() {
        m.text("title", &v);
    }
    if let Some(v) = tag.artist() {
        m.text("artist", &v);
    }
    if let Some(v) = tag.album() {
        m.text("album", &v);
    }
    let year = tag
        .get_string(ItemKey::RecordingDate)
        .or_else(|| tag.get_string(ItemKey::Year))
        .and_then(|d| d.trim().get(..4))
        .and_then(|y| y.parse::<u64>().ok())
        .filter(|y| (1000..=2100).contains(y));
    if let Some(y) = year {
        m.uint("year", y);
    }
    Ok(())
}

// ---- PDF ----

fn pdf(path: &Path, m: &mut Meta) -> io::Result<()> {
    let file = open_file(path)?;
    if file.metadata()?.len() > MAX_PDF {
        return Ok(());
    }
    drop(file);
    let info = lopdf::Document::load_metadata(path).map_err(failed)?;
    if info.page_count > 0 {
        m.uint("pages", u64::from(info.page_count));
    }
    // Encrypted strings would only be noise.
    if !info.encrypted {
        if let Some(t) = &info.title {
            m.text("title", t);
        }
        if let Some(a) = &info.author {
            m.text("author", a);
        }
    }
    Ok(())
}

/// Tiny files made in memory for the tests.
#[cfg(test)]
pub mod fixtures {
    /// One IFD entry: tag, type, count, value bytes (little endian).
    type Entry = (u16, u16, u32, Vec<u8>);

    const ASCII: u16 = 2;
    const SHORT: u16 = 3;
    const LONG: u16 = 4;
    const RATIONAL: u16 = 5;
    const BYTE: u16 = 1;

    fn ascii(tag: u16, s: &str) -> Entry {
        let mut v = s.as_bytes().to_vec();
        v.push(0);
        (tag, ASCII, v.len() as u32, v)
    }
    fn short(tag: u16, n: u16) -> Entry {
        (tag, SHORT, 1, n.to_le_bytes().to_vec())
    }
    fn long(tag: u16, n: u32) -> Entry {
        (tag, LONG, 1, n.to_le_bytes().to_vec())
    }
    fn rationals(tag: u16, list: &[(u32, u32)]) -> Entry {
        let v = list
            .iter()
            .flat_map(|(a, b)| a.to_le_bytes().into_iter().chain(b.to_le_bytes()))
            .collect();
        (tag, RATIONAL, list.len() as u32, v)
    }

    /// The size of an IFD with its out-of-line values.
    fn ifd_size(entries: &[Entry]) -> usize {
        2 + 12 * entries.len()
            + 4
            + entries
                .iter()
                .filter(|e| e.3.len() > 4)
                .map(|e| e.3.len() + e.3.len() % 2)
                .sum::<usize>()
    }

    /// An IFD that will sit at `at` in the TIFF data.
    fn ifd(entries: &[Entry], at: usize) -> Vec<u8> {
        let mut out = (entries.len() as u16).to_le_bytes().to_vec();
        let mut extra = Vec::new();
        let extra_at = at + 2 + 12 * entries.len() + 4;
        for (tag, ty, count, value) in entries {
            out.extend(tag.to_le_bytes());
            out.extend(ty.to_le_bytes());
            out.extend(count.to_le_bytes());
            if value.len() <= 4 {
                out.extend(value);
                out.extend(std::iter::repeat_n(0, 4 - value.len()));
            } else {
                out.extend(((extra_at + extra.len()) as u32).to_le_bytes());
                extra.extend(value);
                if value.len() % 2 == 1 {
                    extra.push(0);
                }
            }
        }
        out.extend(0u32.to_le_bytes());
        out.extend(extra);
        out
    }

    /// A TIFF block with a camera, a date and a place.
    pub fn exif_tiff() -> Vec<u8> {
        let (ifd0_at, exif_at);
        let mut ifd0 = vec![
            ascii(0x010f, "ACME"),
            ascii(0x0110, "Pocket Cam 3"),
            short(0x0112, 6),
            long(0x8769, 0),
            long(0x8825, 0),
        ];
        let exif = vec![
            ascii(0x9003, "2023:07:09 20:36:33"),
            ascii(0x9011, "+08:00"),
            ascii(0xa434, "ACME 24mm f/2"),
        ];
        let gps = vec![
            ascii(0x0001, "N"),
            rationals(0x0002, &[(48, 1), (51, 1), (30, 1)]),
            ascii(0x0003, "E"),
            rationals(0x0004, &[(2, 1), (17, 1), (40, 1)]),
            (0x0005, BYTE, 1, vec![0]),
            rationals(0x0006, &[(35, 1)]),
        ];
        ifd0_at = 8;
        exif_at = ifd0_at + ifd_size(&ifd0);
        let gps_at = exif_at + ifd_size(&exif);
        ifd0[3] = long(0x8769, exif_at as u32);
        ifd0[4] = long(0x8825, gps_at as u32);

        let mut t = b"II*\0".to_vec();
        t.extend((ifd0_at as u32).to_le_bytes());
        t.extend(ifd(&ifd0, ifd0_at));
        t.extend(ifd(&exif, exif_at));
        t.extend(ifd(&gps, gps_at));
        t
    }

    /// A JPEG header (no real picture) of 640 x 480 with EXIF.
    pub fn jpeg_with_exif() -> Vec<u8> {
        let tiff = exif_tiff();
        let mut app1 = b"Exif\0\0".to_vec();
        app1.extend(tiff);
        let mut j = vec![0xff, 0xd8, 0xff, 0xe1];
        j.extend((app1.len() as u16 + 2).to_be_bytes());
        j.extend(app1);
        // SOF0: 8 bits, 480 x 640, 3 components.
        j.extend([0xff, 0xc0, 0, 17, 8, 0x01, 0xe0, 0x02, 0x80, 3]);
        j.extend([1, 0x11, 0, 2, 0x11, 1, 3, 0x11, 1]);
        // SOS and the end.
        j.extend([0xff, 0xda, 0, 8, 1, 1, 0, 0, 0x3f, 0, 0x00, 0xff, 0xd9]);
        j
    }

    /// A PNG header of 3 x 2 pixels (no picture data).
    pub fn png() -> Vec<u8> {
        let mut p = b"\x89PNG\r\n\x1a\n".to_vec();
        p.extend(13u32.to_be_bytes());
        p.extend(b"IHDR");
        p.extend(3u32.to_be_bytes());
        p.extend(2u32.to_be_bytes());
        p.extend([8, 2, 0, 0, 0]);
        p.extend([0, 0, 0, 0]); // CRC (not checked)
        p.extend([0, 0, 0, 0]);
        p.extend(b"IEND");
        p.extend([0xae, 0x42, 0x60, 0x82]);
        p
    }

    /// A JPEG start with nothing valid after it.
    pub fn fake_jpeg() -> Vec<u8> {
        let mut v = vec![0xff, 0xd8, 0xff, 0xe0];
        v.extend((0..200u32).map(|i| (i * 37 % 251) as u8));
        v
    }

    fn mp4_box(kind: &[u8; 4], body: &[u8]) -> Vec<u8> {
        let mut b = (body.len() as u32 + 8).to_be_bytes().to_vec();
        b.extend(kind);
        b.extend(body);
        b
    }

    /// A small MP4: 1280 x 720, 2.5 seconds, made 2021-03-04 05:06:07 UTC.
    pub fn mp4() -> Vec<u8> {
        // Seconds from 1904-01-01 to 2021-03-04 05:06:07 UTC.
        let created: u32 = 1_614_834_367 + 2_082_844_800;
        let mut mvhd = vec![0, 0, 0, 0];
        mvhd.extend(created.to_be_bytes());
        mvhd.extend(created.to_be_bytes());
        mvhd.extend(1000u32.to_be_bytes());
        mvhd.extend(2500u32.to_be_bytes());
        mvhd.extend(0x0001_0000u32.to_be_bytes());
        mvhd.extend([0x01, 0]); // volume
        mvhd.extend([0; 10]);
        for v in [0x0001_0000u32, 0, 0, 0, 0x0001_0000, 0, 0, 0, 0x4000_0000] {
            mvhd.extend(v.to_be_bytes());
        }
        mvhd.extend([0; 24]);
        mvhd.extend(2u32.to_be_bytes());

        let mut tkhd = vec![0, 0, 0, 3];
        tkhd.extend(created.to_be_bytes());
        tkhd.extend(created.to_be_bytes());
        tkhd.extend(1u32.to_be_bytes());
        tkhd.extend([0; 4]);
        tkhd.extend(2500u32.to_be_bytes());
        tkhd.extend([0; 8]);
        tkhd.extend([0; 8]);
        for v in [0x0001_0000u32, 0, 0, 0, 0x0001_0000, 0, 0, 0, 0x4000_0000] {
            tkhd.extend(v.to_be_bytes());
        }
        tkhd.extend((1280u32 << 16).to_be_bytes());
        tkhd.extend((720u32 << 16).to_be_bytes());

        let mut hdlr = vec![0; 8];
        hdlr.extend(b"vide");
        hdlr.extend([0; 13]);
        let mut trak_body = mp4_box(b"tkhd", &tkhd);
        trak_body.extend(mp4_box(b"mdia", &mp4_box(b"hdlr", &hdlr)));
        let trak = mp4_box(b"trak", &trak_body);
        let mut moov_body = mp4_box(b"mvhd", &mvhd);
        moov_body.extend(trak);

        let mut f = mp4_box(b"ftyp", b"isom\0\0\x02\0isomiso2mp41");
        f.extend(mp4_box(b"moov", &moov_body));
        f
    }

    /// An 8 kHz mono WAV of one second of silence, with a title, an artist
    /// and a year in a LIST INFO chunk.
    pub fn wav() -> Vec<u8> {
        fn chunk(id: &[u8; 4], body: &[u8]) -> Vec<u8> {
            let mut c = id.to_vec();
            c.extend((body.len() as u32).to_le_bytes());
            c.extend(body);
            if body.len() % 2 == 1 {
                c.push(0);
            }
            c
        }
        let mut fmt = 1u16.to_le_bytes().to_vec(); // PCM
        fmt.extend(1u16.to_le_bytes());
        fmt.extend(8000u32.to_le_bytes());
        fmt.extend(8000u32.to_le_bytes());
        fmt.extend(1u16.to_le_bytes());
        fmt.extend(8u16.to_le_bytes());
        let mut info = b"INFO".to_vec();
        info.extend(chunk(b"INAM", b"Quiet Song\0"));
        info.extend(chunk(b"IART", b"The Nobodies\0"));
        info.extend(chunk(b"ICRD", b"2019-05-01\0"));
        let mut body = b"WAVE".to_vec();
        body.extend(chunk(b"fmt ", &fmt));
        body.extend(chunk(b"data", &[128u8; 8000]));
        body.extend(chunk(b"LIST", &info));
        let mut f = b"RIFF".to_vec();
        f.extend((body.len() as u32).to_le_bytes());
        f.extend(body);
        f
    }

    /// A one page PDF with a title and an author.
    pub fn pdf() -> Vec<u8> {
        let objects = [
            "<< /Type /Catalog /Pages 2 0 R >>",
            "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
            "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
            "<< /Title (Tax Report) /Author (Pat Smith) >>",
        ];
        let mut out = b"%PDF-1.4\n".to_vec();
        let mut offsets = Vec::new();
        for (i, o) in objects.iter().enumerate() {
            offsets.push(out.len());
            out.extend(format!("{} 0 obj\n{o}\nendobj\n", i + 1).bytes());
        }
        let xref = out.len();
        out.extend(format!("xref\n0 {}\n0000000000 65535 f \n", objects.len() + 1).bytes());
        for o in offsets {
            out.extend(format!("{o:010} 00000 n \n").bytes());
        }
        out.extend(
            format!(
                "trailer\n<< /Size {} /Root 1 0 R /Info 4 0 R >>\nstartxref\n{xref}\n%%EOF\n",
                objects.len() + 1
            )
            .bytes(),
        );
        out
    }
}

#[cfg(test)]
mod tests {
    use super::fixtures::{self, *};
    use super::*;
    use serde_json::json;

    fn run(bytes: &[u8], content_type: &str) -> Found {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f");
        std::fs::write(&path, bytes).unwrap();
        extract_sync(&path, content_type)
    }

    #[test]
    fn jpeg_exif() {
        let f = run(&jpeg_with_exif(), "image/jpeg");
        let m = &f.meta;
        assert_eq!(m["width"], 640);
        assert_eq!(m["height"], 480);
        assert_eq!(m["camera_make"], "ACME");
        assert_eq!(m["camera_model"], "Pocket Cam 3");
        assert_eq!(m["lens"], "ACME 24mm f/2");
        assert_eq!(m["orientation"], 6);
        assert_eq!(m["taken_at"], "2023-07-09T20:36:33+08:00");
        assert_eq!(f.taken_at.as_deref(), Some("2023-07-09T12:36:33Z"));
        let lat = m["gps"]["lat"].as_f64().unwrap();
        let lon = m["gps"]["lon"].as_f64().unwrap();
        assert!((lat - 48.858_333).abs() < 1e-5, "{lat}");
        assert!((lon - 2.294_444).abs() < 1e-5, "{lon}");
        assert_eq!(m["gps"]["alt"].as_f64(), Some(35.0));
    }

    #[test]
    fn jpeg_exif_without_offset_is_kept_as_given() {
        let mut j = jpeg_with_exif();
        // Turn the OffsetTimeOriginal tag into an unused tag.
        let at = j.windows(6).position(|w| w == b"+08:00").unwrap();
        j[at..at + 6].copy_from_slice(b"\0\0\0\0\0\0");
        let f = run(&j, "image/jpeg");
        assert_eq!(f.meta["taken_at"], "2023-07-09T20:36:33");
        assert_eq!(f.taken_at.as_deref(), Some("2023-07-09T20:36:33"));
    }

    #[test]
    fn png_has_only_a_size() {
        let f = run(&png(), "image/png");
        assert_eq!(f.meta, json!({ "width": 3, "height": 2 }));
        assert_eq!(f.taken_at, None);
    }

    #[test]
    fn garbage_gives_nothing() {
        for ct in [
            "image/jpeg",
            "image/png",
            "image/heic",
            "image/webp",
            "image/tiff",
            "image/gif",
            "video/mp4",
            "audio/mpeg",
            "application/pdf",
        ] {
            let f = run(&fake_jpeg(), ct);
            assert_eq!(f.meta, json!({}), "{ct}");
            let f = run(&[], ct);
            assert_eq!(f.meta, json!({}), "{ct}");
        }
        assert_eq!(run(b"hello", "text/plain").meta, json!({}));
    }

    #[test]
    fn mp4_track_info() {
        let f = run(&mp4(), "video/mp4");
        assert_eq!(f.meta["width"], 1280);
        assert_eq!(f.meta["height"], 720);
        assert_eq!(f.meta["duration_s"].as_f64(), Some(2.5));
        assert_eq!(f.meta["taken_at"], "2021-03-04T05:06:07+00:00");
        assert_eq!(f.taken_at.as_deref(), Some("2021-03-04T05:06:07Z"));
    }

    #[test]
    fn truncated_mp4_does_not_panic() {
        let whole = mp4();
        for len in (0..whole.len()).step_by(7) {
            let _ = run(&whole[..len], "video/mp4");
            let _ = run(&whole[..len], "video/quicktime");
        }
    }

    #[test]
    fn wav_tags() {
        let f = run(&wav(), "audio/x-wav");
        assert_eq!(f.meta["duration_s"].as_f64(), Some(1.0));
        assert_eq!(f.meta["title"], "Quiet Song");
        assert_eq!(f.meta["artist"], "The Nobodies");
        assert_eq!(f.meta["year"], 2019);
    }

    #[test]
    fn pdf_info() {
        let f = run(&fixtures::pdf(), "application/pdf");
        assert_eq!(
            f.meta,
            json!({ "pages": 1, "title": "Tax Report", "author": "Pat Smith" })
        );
    }

    #[test]
    fn text_is_cleaned() {
        assert_eq!(clean_text("  a\u{0}b\u{7}\nc  ").as_deref(), Some("abc"));
        assert_eq!(clean_text("\u{0}\u{1} "), None);
        assert_eq!(clean_text(&"é".repeat(500)).unwrap().chars().count(), 200);
    }

    #[test]
    fn numbers_must_be_finite() {
        let mut m = Meta::default();
        m.float("a", f64::NAN);
        m.float("b", f64::INFINITY);
        m.float("c", 1.5);
        m.gps(Some(f64::NAN), Some(1.0), None);
        m.gps(Some(91.0), Some(1.0), None);
        m.gps(Some(1.0), Some(2.0), Some(f64::NEG_INFINITY));
        assert_eq!(
            Value::Object(m.0),
            json!({ "c": 1.5, "gps": { "lat": 1.0, "lon": 2.0 } })
        );
    }

    #[test]
    fn too_big_meta_is_dropped() {
        let mut m = Meta::default();
        for i in 0..40 {
            m.text(&format!("k{i}"), &"x".repeat(200));
        }
        assert_eq!(m.finish().meta, json!({}));
    }

    #[test]
    fn date_keys() {
        assert_eq!(
            taken_at_key("2023-07-09T20:36:33+08:00").as_deref(),
            Some("2023-07-09T12:36:33Z")
        );
        assert_eq!(
            taken_at_key("2023-07-09T20:36:33").as_deref(),
            Some("2023-07-09T20:36:33")
        );
        assert_eq!(taken_at_key("yesterday"), None);
        assert_eq!(
            parse_taken_bound("2023-07-09").as_deref(),
            Some("2023-07-09T00:00:00")
        );
        assert_eq!(
            parse_taken_bound("2023-07-09T10:00:00-02:00").as_deref(),
            Some("2023-07-09T12:00:00")
        );
        for bad in [
            "",
            "2023",
            "2023-13-01",
            "2023-7-9",
            "2023-07-09T10:00:00",
            "x",
        ] {
            assert_eq!(parse_taken_bound(bad), None, "{bad}");
        }
    }

    #[test]
    fn the_reader_stops_at_its_cap() {
        let mut r = Capped {
            inner: io::Cursor::new(vec![1u8; 100]),
            left: 10,
        };
        let mut out = Vec::new();
        r.read_to_end(&mut out).unwrap();
        assert_eq!(out.len(), 10);
    }

    #[tokio::test]
    async fn extract_survives_a_missing_file() {
        let f = extract("/nonexistent/file".into(), "image/jpeg".into(), "id").await;
        assert_eq!(f.meta, json!({}));
    }
}
