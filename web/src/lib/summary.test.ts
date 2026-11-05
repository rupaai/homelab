import { describe, expect, it } from 'vitest';
import { appHealth, appsSummary, summaryParts } from './summary';
import type { Health } from './types';

const app = (...healths: Health[]) => ({ envs: healths.map((health) => ({ health })) }) as never;

describe('appHealth', () => {
	it('takes the worst checked environment', () => {
		expect(appHealth(app('healthy', 'down'))).toBe('down');
		expect(appHealth(app('healthy', 'degraded'))).toBe('degraded');
		expect(appHealth(app('unknown', 'healthy'))).toBe('healthy');
		expect(appHealth(app('unknown', 'unknown'))).toBe('unknown');
		expect(appHealth(app())).toBe('unknown');
	});
});

describe('appsSummary', () => {
	it('counts each app once', () => {
		const s = appsSummary([app('healthy', 'healthy'), app('unknown', 'healthy'), app('degraded', 'down'), app('unknown')]);
		expect(s).toEqual({ total: 4, healthy: 2, problems: 0, down: 1, unchecked: 1 });
	});
});

describe('summaryParts', () => {
	it('says nothing without apps', () => {
		expect(summaryParts(appsSummary([]))).toEqual([]);
	});
	it('keeps healthy quiet and colours only trouble', () => {
		const parts = summaryParts(appsSummary([app('healthy'), app('healthy'), app('healthy'), app('down')]));
		expect(parts).toEqual([
			{ text: '3 apps healthy', tone: 'plain' },
			{ text: '1 down', tone: 'bad' }
		]);
	});
	it('uses the singular', () => {
		expect(summaryParts(appsSummary([app('healthy')]))[0].text).toBe('1 app healthy');
	});
	it('names apps with problems', () => {
		expect(summaryParts(appsSummary([app('degraded')]))).toEqual([{ text: '1 having problems', tone: 'warn' }]);
	});
});
