import { describe, expect, it } from 'vitest';
import { appInitial, bytes, countdown, promotionHelp, deployedLabel, healthLabel, healthSentence, isProblemAction, relativeTime } from './format';

const now = Date.parse('2026-10-14T12:00:00Z');
const ago = (ms: number) => new Date(now - ms).toISOString();
const MIN = 60_000;

describe('relativeTime', () => {
	it('says just now for the last moments', () => expect(relativeTime(ago(10_000), now)).toBe('just now'));
	it('uses minutes', () => expect(relativeTime(ago(5 * MIN), now)).toBe('5 min ago'));
	it('uses hours', () => {
		expect(relativeTime(ago(60 * MIN), now)).toBe('1 hour ago');
		expect(relativeTime(ago(3 * 60 * MIN), now)).toBe('3 hours ago');
	});
	it('uses yesterday and days', () => {
		expect(relativeTime(ago(26 * 60 * MIN), now)).toBe('yesterday');
		expect(relativeTime(ago(4 * 24 * 60 * MIN), now)).toBe('4 days ago');
	});
	it('falls back to a date for old times', () => expect(relativeTime(ago(60 * 24 * 60 * MIN), now)).toMatch(/2026/));
	it('copes with missing or bad input', () => {
		expect(relativeTime(null, now)).toBe('');
		expect(relativeTime('nonsense', now)).toBe('');
	});
});

describe('health words', () => {
	it('labels each health', () => {
		expect(healthLabel('healthy')).toBe('Healthy');
		expect(healthLabel('degraded')).toBe('Having problems');
		expect(healthLabel('down')).toBe('Down');
		expect(healthLabel('unknown')).toBe('Not checked yet');
		expect(healthLabel('something-new')).toBe('Not checked yet');
	});
	it('writes plain sentences', () => {
		expect(healthSentence('Movie Night', 'down')).toBe('Movie Night is down');
		expect(healthSentence('Movie Night', 'healthy')).toBe('Movie Night is running fine');
	});
});

describe('small helpers', () => {
	it('names deploy kinds', () => {
		expect(deployedLabel('promote')).toBe('Promoted');
		expect(deployedLabel('rollback')).toBe('Rolled back');
		expect(deployedLabel('deploy')).toBe('Deployed');
	});
	it('spots problem events', () => {
		expect(isProblemAction('promote.failed')).toBe(true);
		expect(isProblemAction('deploy.unhealthy')).toBe(true);
		expect(isProblemAction('deploy.done')).toBe(false);
	});
	it('makes an initial', () => expect(appInitial(' movie night')).toBe('M'));
});

describe('countdown', () => {
	const at = (ms: number) => new Date(now + ms).toISOString();
	it('counts minutes', () => expect(countdown(at(12 * MIN), now)).toBe('in 12 min'));
	it('counts hours', () => {
		expect(countdown(at(60 * MIN), now)).toBe('in 1 hour');
		expect(countdown(at(3 * 60 * MIN), now)).toBe('in 3 hours');
	});
	it('says any moment now when due or past', () => {
		expect(countdown(at(10_000), now)).toBe('any moment now');
		expect(countdown(at(-5 * MIN), now)).toBe('any moment now');
	});
	it('copes with bad input', () => {
		expect(countdown(null, now)).toBe('');
		expect(countdown('nope', now)).toBe('');
	});
});

describe('promotionHelp', () => {
	it('names the delay for auto', () => expect(promotionHelp('auto', '30m')).toContain('healthy for 30m'));
});

describe('bytes', () => {
	it('reads like people write sizes', () => {
		expect(bytes(512)).toBe('512 B');
		expect(bytes(182e6)).toBe('182 MB');
		expect(bytes(5.1e9)).toBe('5.1 GB');
		expect(bytes(-1)).toBe('–');
	});
});
