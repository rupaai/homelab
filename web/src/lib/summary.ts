/** The one-line summary at the top of Apps. Pure, so it can be tested without a browser. */
import type { AppView, Health } from './types';

export interface AppsSummary {
	total: number;
	healthy: number;
	problems: number;
	down: number;
	unchecked: number;
}

/** One app's health: the worst of its environments that were checked. */
export function appHealth(app: Pick<AppView, 'envs'>): Health {
	const seen = app.envs.map((e) => e.health);
	if (seen.includes('down')) return 'down';
	if (seen.includes('degraded')) return 'degraded';
	if (seen.includes('healthy')) return 'healthy';
	return 'unknown';
}

export function appsSummary(apps: Pick<AppView, 'envs'>[]): AppsSummary {
	const s: AppsSummary = { total: apps.length, healthy: 0, problems: 0, down: 0, unchecked: 0 };
	for (const app of apps) {
		const h = appHealth(app);
		if (h === 'healthy') s.healthy++;
		else if (h === 'degraded') s.problems++;
		else if (h === 'down') s.down++;
		else s.unchecked++;
	}
	return s;
}

const apps = (n: number) => `${n} ${n === 1 ? 'app' : 'apps'}`;

/** The pieces of the line, each with a tone so only trouble gets colour. */
export function summaryParts(s: AppsSummary): { text: string; tone: 'plain' | 'warn' | 'bad' }[] {
	if (s.total === 0) return [];
	const parts: { text: string; tone: 'plain' | 'warn' | 'bad' }[] = [];
	if (s.healthy > 0 || (s.problems === 0 && s.down === 0 && s.unchecked === 0)) parts.push({ text: `${apps(s.healthy)} healthy`, tone: 'plain' });
	if (s.problems > 0) parts.push({ text: `${s.problems} having problems`, tone: 'warn' });
	if (s.down > 0) parts.push({ text: `${s.down} down`, tone: 'bad' });
	if (s.unchecked > 0) parts.push({ text: `${s.unchecked} not checked yet`, tone: 'plain' });
	return parts;
}
