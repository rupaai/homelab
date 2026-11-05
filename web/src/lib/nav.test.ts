import { describe, expect, it } from 'vitest';
import { isAdmin, navTabs } from './nav';

const labels = (roles: string[]) => navTabs(roles).map((t) => t.label);
const active = (roles: string[], path: string) => navTabs(roles).filter((t) => t.match(path)).map((t) => t.label);

describe('navTabs', () => {
	it('gives owners and admins four tabs', () => {
		expect(labels(['owner'])).toEqual(['Apps', 'Activity', 'Storage', 'Settings']);
		expect(labels(['admin'])).toEqual(['Apps', 'Activity', 'Storage', 'Settings']);
		expect(navTabs(['owner']).map((t) => t.href)).toEqual(['/', '/activity', '/files', '/settings']);
	});
	it('gives builders and family only Apps', () => {
		expect(labels(['builder'])).toEqual(['Apps']);
		expect(labels(['family'])).toEqual(['Apps']);
		expect(labels([])).toEqual(['Apps']);
	});
	it('tells admins apart', () => {
		expect(isAdmin(['owner'])).toBe(true);
		expect(isAdmin(['builder'])).toBe(false);
	});
	it('matches Apps for the old and new app addresses', () => {
		for (const p of ['/', '/home', '/apps/movie-night', '/apps/movie-night/logs/dev', '/apps/movie-night/secrets']) {
			expect(active(['owner'], p)).toEqual(['Apps']);
		}
	});
	it('matches Activity for events and logs', () => {
		expect(active(['owner'], '/activity')).toEqual(['Activity']);
		expect(active(['owner'], '/logs')).toEqual(['Activity']);
	});
	it('matches Storage for files and databases', () => {
		for (const p of ['/files', '/files/photos', '/databases', '/databases/movie-night']) {
			expect(active(['owner'], p)).toEqual(['Storage']);
		}
	});
	it('matches Settings for its sub pages and the old addresses', () => {
		for (const p of ['/settings', '/settings/keys', '/settings/homelab', '/resources', '/people', '/me']) {
			expect(active(['owner'], p)).toEqual(['Settings']);
		}
	});
	it('does not match by prefix alone', () => {
		expect(active(['owner'], '/filesystem')).toEqual([]);
		expect(active(['owner'], '/media')).toEqual([]);
	});
});
