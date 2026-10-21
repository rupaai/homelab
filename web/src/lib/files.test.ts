import { describe, expect, it } from 'vitest';
import { fileCount, folderDate, folderOwner, folderProblem, freeText, isImage, lengthText, metaLines, metaSummary, parseTags, usedPercent } from './files';

describe('folderProblem', () => {
	it('accepts good names', () => {
		for (const n of ['photos', 'a', '2024-trip', 'my_files', 'a'.repeat(32)]) expect(folderProblem(n)).toBe('');
	});
	it('explains bad names', () => {
		expect(folderProblem('')).toMatch(/name/);
		for (const n of ['Photos', '-x', '_x', 'a b', 'a.b', 'a'.repeat(33)]) expect(folderProblem(n)).not.toBe('');
	});
});

describe('freeText', () => {
	it('says what is free', () => expect(freeText({ name: 'main', used: 880e9, total: 1e12 })).toBe('main · 120 GB free of 1.0 TB'));
	it('never goes below zero', () => expect(freeText({ name: 'x', used: 20, total: 10 })).toBe('x · 0 B free of 10 B'));
});

describe('usedPercent', () => {
	it('rounds and clamps', () => {
		expect(usedPercent({ used: 1, total: 3 })).toBe(33);
		expect(usedPercent({ used: 5, total: 0 })).toBe(0);
		expect(usedPercent({ used: 50, total: 10 })).toBe(100);
	});
});

describe('isImage', () => {
	it('knows pictures a browser can show', () => {
		expect(isImage('image/png')).toBe(true);
		expect(isImage('image/jpeg')).toBe(true);
		expect(isImage('application/pdf')).toBe(false);
		expect(isImage('image/tiff')).toBe(false);
	});
});

describe('parseTags', () => {
	it('trims, drops empties and repeats', () => expect(parseTags(' a, b,,a ,')).toEqual(['a', 'b']));
	it('is empty for nothing', () => expect(parseTags('')).toEqual([]));
});

describe('folderOwner', () => {
	it('names the app, or the admins', () => {
		expect(folderOwner({ app: 'photos', env: 'prod' })).toBe('photos · prod');
		expect(folderOwner({ app: '', env: '' })).toBe('Admins');
	});
});

describe('fileCount', () => {
	it('pluralises', () => {
		expect(fileCount(1)).toBe('1 file');
		expect(fileCount(0)).toBe('0 files');
	});
});

describe('folderDate', () => {
	it('keeps date text only', () => {
		expect(folderDate('2026-01-02T03:04:05Z')).toBe('2026-01-02T03:04:05Z');
		expect(folderDate(1767323045)).toBeNull();
		expect(folderDate('soon')).toBeNull();
		expect(folderDate(undefined)).toBeNull();
	});
});

describe('metaLines', () => {
	const get = (lines: ReturnType<typeof metaLines>, label: string) => lines.find((l) => l.label === label);
	it('gives nothing for missing or empty meta', () => {
		expect(metaLines(undefined)).toEqual([]);
		expect(metaLines(null)).toEqual([]);
		expect(metaLines({})).toEqual([]);
	});
	it('ignores unknown keys and bad values', () => {
		expect(metaLines({ colour: 'red', width: 'wide', pages: 'x' } as never)).toEqual([]);
	});
	it('describes a photo', () => {
		const lines = metaLines({ width: 4032, height: 3024, taken_at: '2026-10-03T14:21:07Z', camera_make: 'Apple', camera_model: 'iPhone 15 Pro', lens: '6.86mm' });
		expect(get(lines, 'Size in pixels')?.value).toBe('4032 × 3024');
		expect(get(lines, 'Taken')?.value).not.toBe('');
		expect(get(lines, 'Camera')?.value).toBe('Apple iPhone 15 Pro');
		expect(get(lines, 'Lens')?.value).toBe('6.86mm');
	});
	it('does not repeat the make', () => {
		expect(get(metaLines({ camera_make: 'Canon', camera_model: 'Canon EOS R6' }), 'Camera')?.value).toBe('Canon EOS R6');
		expect(get(metaLines({ camera_make: 'canon', camera_model: 'CANON EOS R6' }), 'Camera')?.value).toBe('CANON EOS R6');
		expect(get(metaLines({ camera_make: 'Canon' }), 'Camera')?.value).toBe('Canon');
		expect(get(metaLines({ camera_model: 'EOS R6' }), 'Camera')?.value).toBe('EOS R6');
	});
	it('describes a video, a document and a song', () => {
		expect(get(metaLines({ duration_s: 151 }), 'Length')?.value).toBe('2:31');
		expect(get(metaLines({ pages: 12, author: 'Ann', title: 'Policy' }), 'Pages')?.value).toBe('12');
		const song = metaLines({ title: 'Song', artist: 'Band', album: 'Record', year: 1999 });
		expect(song.map((l) => l.label)).toEqual(['Title', 'Artist', 'Album', 'Year']);
	});
	it('links the place to a map', () => {
		const place = get(metaLines({ gps: { lat: 55.675941, lon: 12.565532, alt: 3 } }), 'Place');
		expect(place?.value).toBe('55.67594, 12.56553');
		expect(place?.link).toEqual({ text: 'Open map', href: 'https://www.openstreetmap.org/?mlat=55.67594&mlon=12.56553' });
	});
	it('skips a place that is not on Earth', () => {
		expect(get(metaLines({ gps: { lat: 200, lon: 0 } }), 'Place')).toBeUndefined();
	});
	it('keeps markup as plain text', () => {
		expect(get(metaLines({ title: '<img src=x onerror=alert(1)>' }), 'Title')?.value).toBe('<img src=x onerror=alert(1)>');
	});
});

describe('lengthText', () => {
	it('uses m:ss under an hour and h:mm:ss after', () => {
		expect(lengthText(0)).toBe('0:00');
		expect(lengthText(59.6)).toBe('1:00');
		expect(lengthText(151)).toBe('2:31');
		expect(lengthText(3725)).toBe('1:02:05');
	});
});

describe('metaSummary', () => {
	it('is empty with nothing to say', () => expect(metaSummary({})).toBe(''));
	it('shows the length alone', () => expect(metaSummary({ duration_s: 151 })).toBe('2:31'));
	it('shows pages', () => expect(metaSummary({ pages: 1 })).toBe('1 page'));
	it('joins date and camera', () => {
		const s = metaSummary({ taken_at: '2026-10-03T14:21:07Z', camera_make: 'Apple', camera_model: 'iPhone 15 Pro' });
		expect(s).toMatch(/^Taken .*2026.* · Apple iPhone 15 Pro$/);
	});
});
