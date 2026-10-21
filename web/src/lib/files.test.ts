import { describe, expect, it } from 'vitest';
import { fileCount, folderDate, folderOwner, folderProblem, freeText, isImage, parseTags, usedPercent } from './files';

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
