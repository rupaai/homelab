import { absoluteTime, bytes, shortDate } from './format';
import type { FileFolder, FileMeta, StorageDisk } from './types';

const FOLDER_NAME = /^[a-z0-9][a-z0-9_-]{0,31}$/;

/** Why a folder name won't work, in plain words, or '' when it will. */
export function folderProblem(name: string): string {
	if (!name) return 'Give the folder a name.';
	if (name.length > 32) return 'Use 32 letters or fewer.';
	if (!FOLDER_NAME.test(name)) return 'Use only small letters, numbers, dashes and underscores. Start with a letter or number.';
	return '';
}

/** "main · 120 GB free of 1 TB" */
export const freeText = (d: Pick<StorageDisk, 'name' | 'used' | 'total'>): string =>
	`${d.name} · ${bytes(Math.max(0, d.total - d.used))} free of ${bytes(d.total)}`;

/** How full a disk is, 0 to 100. */
export function usedPercent(d: Pick<StorageDisk, 'used' | 'total'>): number {
	if (!(d.total > 0)) return 0;
	return Math.min(100, Math.max(0, Math.round((d.used / d.total) * 100)));
}

export const isImage = (contentType: string): boolean => /^image\/(png|jpe?g|gif|webp|avif|svg\+xml)/i.test(contentType);

/** "a, b,,a" becomes ["a", "b"]. */
export function parseTags(text: string): string[] {
	const out: string[] = [];
	for (const t of text.split(',')) {
		const tag = t.trim();
		if (tag && !out.includes(tag)) out.push(tag);
	}
	return out;
}

export const folderOwner = (f: Pick<FileFolder, 'app' | 'env'>): string => (f.app ? (f.env ? `${f.app} · ${f.env}` : f.app) : 'Admins');

export const fileCount = (n: number): string => `${n} ${n === 1 ? 'file' : 'files'}`;

/** The folder's creation time when the server sent a date text; otherwise nothing to show. */
export function folderDate(created: string | number | null | undefined): string | null {
	return typeof created === 'string' && created !== '' && !Number.isNaN(Date.parse(created)) ? created : null;
}

/** Hands a fetched file to the browser as a download. */
export function saveBlob(blob: Blob, name: string): void {
	const url = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = name;
	a.click();
	setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export interface MetaLine { label: string; value: string; link?: { text: string; href: string } }

const text = (v: unknown): string => (typeof v === 'string' ? v.trim() : '');
const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);

/** "Apple iPhone 15 Pro", but not "Apple Apple iPhone 15 Pro". */
function cameraName(meta: FileMeta): string {
	const make = text(meta.camera_make);
	const model = text(meta.camera_model);
	if (make && model) return model.toLowerCase().startsWith(make.toLowerCase()) ? model : `${make} ${model}`;
	return make || model;
}

/** 151 becomes "2:31"; 3725 becomes "1:02:05". */
export function lengthText(seconds: number): string {
	const s = Math.max(0, Math.round(seconds));
	const h = Math.floor(s / 3600);
	const m = Math.floor((s % 3600) / 60);
	const sec = String(s % 60).padStart(2, '0');
	return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

/** A link to a spot on the map. The numbers are always plain numbers, never text from the file. */
export const mapLink = (lat: number, lon: number): string => `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lon}`;

/** What Homelab read from a file, in plain words. Missing meta and unknown keys give nothing. */
export function metaLines(meta: FileMeta | null | undefined): MetaLine[] {
	const m = (meta ?? {}) as FileMeta;
	const out: MetaLine[] = [];
	const w = num(m.width);
	const h = num(m.height);
	if (w && h) out.push({ label: 'Size in pixels', value: `${w} × ${h}` });
	const taken = text(m.taken_at) ? absoluteTime(m.taken_at) : '';
	if (taken) out.push({ label: 'Taken', value: taken });
	const camera = cameraName(m);
	if (camera) out.push({ label: 'Camera', value: camera });
	if (text(m.lens)) out.push({ label: 'Lens', value: text(m.lens) });
	const len = num(m.duration_s);
	if (len !== undefined && len >= 0) out.push({ label: 'Length', value: lengthText(len) });
	const pages = num(m.pages);
	if (pages) out.push({ label: 'Pages', value: String(pages) });
	if (text(m.title)) out.push({ label: 'Title', value: text(m.title) });
	if (text(m.artist)) out.push({ label: 'Artist', value: text(m.artist) });
	if (text(m.album)) out.push({ label: 'Album', value: text(m.album) });
	const year = num(m.year);
	if (year) out.push({ label: 'Year', value: String(year) });
	if (text(m.author)) out.push({ label: 'Author', value: text(m.author) });
	const lat = num(m.gps?.lat);
	const lon = num(m.gps?.lon);
	if (lat !== undefined && lon !== undefined && Math.abs(lat) <= 90 && Math.abs(lon) <= 180) {
		out.push({ label: 'Place', value: `${lat.toFixed(5)}, ${lon.toFixed(5)}`, link: { text: 'Open map', href: mapLink(Number(lat.toFixed(5)), Number(lon.toFixed(5))) } });
	}
	return out;
}

/** One short line for the file list, like "Taken 3 Oct 2026 · iPhone 15 Pro" or "2:31". Empty when there's nothing to say. */
export function metaSummary(meta: FileMeta | null | undefined): string {
	const m = (meta ?? {}) as FileMeta;
	const parts: string[] = [];
	const date = text(m.taken_at) ? shortDate(m.taken_at) : '';
	if (date) parts.push(`Taken ${date}`);
	const camera = cameraName(m);
	if (camera) parts.push(camera);
	const len = num(m.duration_s);
	if (len !== undefined && len >= 0) parts.push(lengthText(len));
	const pages = num(m.pages);
	if (pages) parts.push(`${pages} ${pages === 1 ? 'page' : 'pages'}`);
	if (text(m.artist)) parts.push(text(m.artist));
	return parts.join(' · ');
}
