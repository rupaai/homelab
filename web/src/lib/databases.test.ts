import { afterEach, describe, expect, it, vi } from 'vitest';
import { PROD_COPY_NOTE, changeTime, changeWords, confirmLabel, hasMoreChanges, percent, personName, writeHint, copyKindWords, dbAddress, dbStatusWords, loginProblem, needsConfirm, rowsRange, saveLogin, savedLogin, tableCount, tunnelCommand } from './databases';

describe('dbStatusWords', () => {
	it('says it plainly', () => {
		expect(dbStatusWords('ok')).toBe('Working');
		expect(dbStatusWords('empty')).toBe('Nothing saved yet');
		expect(dbStatusWords('down')).toBe('Not answering');
	});
});

describe('tableCount', () => {
	it('pluralises', () => {
		expect(tableCount(1)).toBe('1 table');
		expect(tableCount(0)).toBe('0 tables');
	});
});

describe('copyKindWords', () => {
	it('explains the kinds, and keeps unknown ones', () => {
		expect(copyKindWords('manual')).toBe('Made by hand');
		expect(copyKindWords('before-restore')).toBe('Before a restore');
		expect(copyKindWords('later')).toBe('later');
	});
});

describe('needsConfirm', () => {
	it('asks only for changes to prod', () => {
		expect(needsConfirm('prod', true)).toBe(true);
		expect(needsConfirm('prod', false)).toBe(false);
		expect(needsConfirm('dev', true)).toBe(false);
	});
});

describe('connect', () => {
	it('builds the tunnel and the address', () => {
		expect(tunnelCommand(17015, ' alex ', 'homelab.local')).toBe('ssh -N -L 15432:127.0.0.1:17015 alex@homelab.local');
		expect(dbAddress('pw-1')).toBe('postgres://app:pw-1@127.0.0.1:15432/app');
	});
	it('only takes plain logins', () => {
		expect(loginProblem('alex')).toBe('');
		expect(loginProblem('')).not.toBe('');
		expect(loginProblem('a b; rm')).not.toBe('');
	});
});

describe('saved login', () => {
	afterEach(() => vi.unstubAllGlobals());
	it('keeps the login', () => {
		const store = new Map<string, string>();
		vi.stubGlobal('localStorage', { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v) });
		expect(savedLogin()).toBe('');
		saveLogin(' alex ');
		expect(savedLogin()).toBe('alex');
	});
	it('works when storage is blocked', () => {
		const blocked = () => { throw new Error('blocked'); };
		vi.stubGlobal('localStorage', { getItem: blocked, setItem: blocked });
		expect(savedLogin()).toBe('');
		expect(() => saveLogin('alex')).not.toThrow();
	});
});

describe('rowsRange', () => {
	it('counts from one', () => {
		expect(rowsRange(0, 50)).toBe('Rows 1 to 50');
		expect(rowsRange(50, 40)).toBe('Rows 51 to 90');
		expect(rowsRange(0, 0)).toBe('No rows');
	});
});

describe('import and changes helpers', () => {
	it('shows upload progress as a percent', () => {
		expect(percent(0.256)).toBe('26%');
		expect(percent(2)).toBe('100%');
		expect(percent(-1)).toBe('0%');
	});
	it('says the write words', () => {
		expect(writeHint(true)).toBe('This can change data.');
		expect(writeHint(false)).toBe('Read only. Nothing is changed.');
		expect(PROD_COPY_NOTE).toBe('On prod, Homelab makes a copy first, so you can undo with Restore.');
		expect(confirmLabel('prod')).toBe('Type prod to run it');
	});
	it('names people, or keeps the id', () => {
		expect(personName([{ id: 'p1', name: 'Alex' }], 'p1')).toBe('Alex');
		expect(personName([], 'p9')).toBe('p9');
	});
	it('turns change times and results into words', () => {
		expect(changeTime(0)).toBe('1970-01-01T00:00:00.000Z');
		expect(changeWords(true, 1)).toBe('Worked · 1 row');
		expect(changeWords(true, 3)).toBe('Worked · 3 rows');
		expect(changeWords(false, 0)).toBe('Failed');
	});
	it('offers more only after a full page', () => {
		expect(hasMoreChanges(50, 50)).toBe(true);
		expect(hasMoreChanges(7, 50)).toBe(false);
	});
});
