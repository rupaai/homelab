import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyKindWords, dbAddress, dbStatusWords, loginProblem, needsConfirm, rowsRange, saveLogin, savedLogin, tableCount, tunnelCommand } from './databases';

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
