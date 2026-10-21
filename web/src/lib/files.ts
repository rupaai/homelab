import { bytes } from './format';
import type { FileFolder, StorageDisk } from './types';

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
