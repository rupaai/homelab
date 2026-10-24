import { describe, expect, it } from 'vitest';
import { FILTER_LEVELS, LEVELS, RANGES, addCounts, chipFor, countSummary, extraText, fixLogPage, levelInfo, lineTime, logsHref, logsParams, mergeNewer, newestId, noCounts, sinceFor } from './logs';
import type { LogLine } from './types';

const line = (id: number, o: Partial<LogLine> = {}): LogLine => ({ id, time: '2026-10-23T22:00:00.123Z', level: 'info', msg: 'hi', app: 'a', env: 'prod', service: 'app', stream: 'stdout', extra: {}, ...o });

describe('levels', () => {
	it('match the contract', () => {
		expect(LEVELS.map((l) => [l.level, l.number, l.tag])).toEqual([['unknown', 0, '---'], ['debug', 1, 'DBG'], ['info', 2, 'INF'], ['warn', 3, 'WRN'], ['error', 4, 'ERR'], ['fatal', 5, 'FTL']]);
		expect(LEVELS.map((l) => l.colour)).toEqual(['grey', 'blue', 'green', 'yellow', 'red', 'purple']);
	});
	it('give every level an icon and a word', () => {
		for (const l of LEVELS) { expect(l.icon).not.toBe(''); expect(l.word).not.toBe(''); }
		expect(new Set(LEVELS.map((l) => l.icon)).size).toBe(LEVELS.length);
	});
	it('treat a level it does not know as unknown', () => expect(levelInfo('loud').level).toBe('unknown'));
	it('list the filters loudest first', () => expect(FILTER_LEVELS[0].level).toBe('fatal'));
});

describe('sinceFor', () => {
	it('counts back from now', () => {
		const now = Date.parse('2026-10-23T12:00:00Z');
		expect(sinceFor('15m', now)).toBe('2026-10-23T11:45:00.000Z');
		expect(sinceFor('14d', now)).toBe('2026-10-09T12:00:00.000Z');
	});
	it('has nothing for an unknown range', () => expect(sinceFor('1y')).toBeUndefined());
	it('offers the five ranges', () => expect(RANGES.map((r) => r.id)).toEqual(['15m', '1h', '24h', '7d', '14d']));
});

describe('logsParams', () => {
	it('is empty for nothing', () => expect(logsParams({}).toString()).toBe(''));
	it('sends each filter under the name the server reads', () => {
		const p = logsParams({ q: ' boom ', app: 'x', env: 'dev', service: 'app', level: ['error', 'fatal'], minLevel: 'warn', since: '2026-01-01T00:00:00Z', until: '1730000000000', before: 41, after: 7, limit: 50 });
		expect(Object.fromEntries(p)).toEqual({ q: 'boom', app: 'x', env: 'dev', service: 'app', level: 'error,fatal', min_level: 'warn', since: '2026-01-01T00:00:00Z', until: '1730000000000', before: '41', after: '7', limit: '50' });
	});
	it('leaves out empty values but keeps a zero', () => {
		expect(logsParams({ q: '  ', app: '', level: [], before: 0 }).toString()).toBe('before=0');
	});
});

describe('fixLogPage', () => {
	it('fills in what an older or empty answer leaves out', () => {
		expect(fixLogPage(null)).toEqual({ lines: [], counts: noCounts(), next: null });
		expect(fixLogPage({ lines: null as never }).lines).toEqual([]);
	});
	it('keeps the lines and counts', () => {
		const r = fixLogPage({ lines: [line(42, { level: 'error' })], counts: { ...noCounts(), error: 2 }, next: '41' });
		expect(r.lines[0].id).toBe(42);
		expect(r.counts.error).toBe(2);
		expect(r.next).toBe('41');
	});
	it('turns a strange level into unknown and a missing extra into an object', () => {
		const r = fixLogPage({ lines: [{ ...line(1), level: 'loud' as never, extra: undefined as never }] });
		expect(r.lines[0].level).toBe('unknown');
		expect(r.lines[0].extra).toEqual({});
	});
});

describe('chipFor', () => {
	it('names the app and env', () => expect(chipFor({ app: 'northern-lights', env: 'prod', service: 'app' })).toEqual({ text: 'northern-lights · prod', homelab: false }));
	it('names the service for Homelab lines', () => expect(chipFor({ app: 'homelab', env: '', service: 'files' })).toEqual({ text: 'homelab · files', homelab: true }));
	it('copes with missing parts', () => {
		expect(chipFor({ app: 'homelab', env: '', service: '' }).text).toBe('homelab');
		expect(chipFor({ app: '', env: '', service: '' }).text).toBe('unknown');
	});
});

describe('counts', () => {
	it('summarises the levels that have lines, loudest first', () => expect(countSummary({ ...noCounts(), warn: 12, error: 3 })).toBe('3 ERR · 12 WRN'));
	it('is empty with no lines', () => expect(countSummary(noCounts())).toBe(''));
	it('adds', () => expect(addCounts({ ...noCounts(), info: 1 }, { ...noCounts(), info: 2, error: 1 })).toMatchObject({ info: 3, error: 1, debug: 0 }));
});

describe('mergeNewer', () => {
	it('puts new lines on top without repeats', () => {
		const out = mergeNewer([line(5), line(4)], [line(7), line(6), line(5)]);
		expect(out.map((l) => l.id)).toEqual([7, 6, 5, 4]);
	});
	it('finds the newest id', () => {
		expect(newestId([line(3), line(9), line(5)])).toBe(9);
		expect(newestId([])).toBeUndefined();
	});
});

describe('small helpers', () => {
	it('shows the time of day', () => {
		expect(lineTime('2026-10-23T22:00:00.123Z')).toMatch(/^\d{2}:\d{2}:\d{2}$/);
		expect(lineTime('soon')).toBe('');
	});
	it('shows extra only when there is some', () => {
		expect(extraText({})).toBe('');
		expect(extraText(undefined)).toBe('');
		expect(extraText({ a: 1 })).toBe('{\n  "a": 1\n}');
	});
	it('links to an app environment', () => {
		expect(logsHref('northern-lights', 'prod')).toBe('/logs?app=northern-lights&env=prod');
		expect(logsHref('x')).toBe('/logs?app=x');
	});
});
