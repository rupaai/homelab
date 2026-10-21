//! Small helpers for names and for the folders on the storage disks.

use std::fs::DirBuilder;
use std::io;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};

/// True for `[a-z0-9][a-z0-9_-]*` of at most `max` bytes.
pub fn valid_name(s: &str, max: usize) -> bool {
    let b = s.as_bytes();
    !b.is_empty()
        && b.len() <= max
        && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit())
        && b.iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'_' || *c == b'-')
}

/// A file name that is safe to put in a path or a header.
/// Keeps ASCII letters, digits, `.`, `_` and `-`; everything else becomes `_`.
pub fn safe_name(name: &str) -> String {
    let s: String = name
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-') {
                c
            } else {
                '_'
            }
        })
        .collect();
    // Only ASCII is left, so cutting at a byte is fine.
    let s = s.trim_start_matches('.');
    let s = &s[..s.len().min(100)];
    if s.is_empty() {
        "file".to_string()
    } else {
        s.to_string()
    }
}

/// The year and month (UTC) of a unix time in seconds.
pub fn year_month(secs: u64) -> (u32, u32) {
    // Days since 1970-01-01 to a calendar date (Howard Hinnant's algorithm).
    let z = (secs / 86400) as i64 + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    (year as u32, month as u32)
}

/// Makes sure `path` is a real folder (not a symlink), creating it if needed.
pub fn ensure_dir(path: &Path) -> io::Result<()> {
    match DirBuilder::new().mode(0o700).create(path) {
        Ok(()) => {}
        Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {}
        Err(e) => return Err(e),
    }
    // symlink_metadata does not follow links, so a link is never "a dir".
    if std::fs::symlink_metadata(path)?.is_dir() {
        Ok(())
    } else {
        Err(io::Error::other(format!(
            "{} is not a real folder",
            path.display()
        )))
    }
}

/// Makes the folders for an upload. Returns the temp folder and the final one.
pub fn make_dirs(
    disk: &Path,
    folder: &str,
    year: u32,
    month: u32,
) -> io::Result<(PathBuf, PathBuf)> {
    let root = disk.join("homelab-files");
    let tmp = root.join(".tmp");
    let dir = root
        .join(folder)
        .join(format!("{year:04}"))
        .join(format!("{month:02}"));
    ensure_dir(&root)?;
    ensure_dir(&tmp)?;
    for p in dir
        .ancestors()
        .take(3)
        .collect::<Vec<_>>()
        .into_iter()
        .rev()
    {
        ensure_dir(p)?;
    }
    Ok((tmp, dir))
}

/// Opens a stored file for reading. Refuses symlinks and anything not a file.
pub fn open_file(path: &Path) -> io::Result<std::fs::File> {
    let file = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW)
        .open(path)?;
    if file.metadata()?.is_file() {
        Ok(file)
    } else {
        Err(io::Error::other("not a regular file"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn safe_names() {
        assert_eq!(safe_name("photo.jpg"), "photo.jpg");
        assert_eq!(safe_name("../../etc/passwd"), "_.._etc_passwd");
        assert_eq!(safe_name(".hidden"), "hidden");
        assert_eq!(safe_name("..."), "file");
        assert_eq!(safe_name(""), "file");
        assert_eq!(safe_name("a b\"c.txt"), "a_b_c.txt");
        assert_eq!(safe_name("é.png"), "_.png");
        assert_eq!(safe_name(&"a".repeat(300)).len(), 100);
    }

    #[test]
    fn names() {
        assert!(valid_name("photos", 63));
        assert!(valid_name("a", 63));
        assert!(valid_name("0a_b-c", 63));
        for bad in ["", "..", "-a", "_a", "A", "a b", "a/b", "a.b", "é"] {
            assert!(!valid_name(bad, 63), "{bad}");
        }
        assert!(valid_name(&"a".repeat(63), 63));
        assert!(!valid_name(&"a".repeat(64), 63));
        assert!(!valid_name(&"a".repeat(33), 32));
    }

    #[test]
    fn dates() {
        assert_eq!(year_month(0), (1970, 1));
        assert_eq!(year_month(951_782_400), (2000, 2)); // 2000-02-29
        assert_eq!(year_month(1_700_000_000), (2023, 11));
        assert_eq!(year_month(1_709_251_199), (2024, 2)); // 2024-02-29 23:59:59
        assert_eq!(year_month(1_709_251_200), (2024, 3));
    }
}
