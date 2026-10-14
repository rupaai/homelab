import type { DeployKind, EnvName, Health } from './types';

export function relativeTime(iso: string | null | undefined, now: number = Date.now()): string {
	if (!iso) return '';
	const t = Date.parse(iso);
	if (Number.isNaN(t)) return '';
	const sec = Math.round((now - t) / 1000);
	if (sec < 45) return 'just now';
	const min = Math.round(sec / 60);
	if (min < 60) return `${min} min ago`;
	const hr = Math.round(min / 60);
	if (hr < 24) return `${hr} ${hr === 1 ? 'hour' : 'hours'} ago`;
	const day = Math.round(hr / 24);
	if (day === 1) return 'yesterday';
	if (day < 14) return `${day} days ago`;
	return new Date(t).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

export function absoluteTime(iso: string | null | undefined): string {
	if (!iso) return '';
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
}

export function healthLabel(h: Health | string): string {
	switch (h) {
		case 'healthy':
			return 'Healthy';
		case 'degraded':
			return 'Having problems';
		case 'down':
			return 'Down';
		default:
			return 'Not checked yet';
	}
}

/** A whole sentence for an environment's health, in plain words. */
export function healthSentence(name: string, h: Health | string): string {
	switch (h) {
		case 'healthy':
			return `${name} is running fine`;
		case 'degraded':
			return `${name} is having problems`;
		case 'down':
			return `${name} is down`;
		default:
			return `${name} hasn't been checked yet`;
	}
}

export const envLabel = (e: EnvName | string): string => (e === 'prod' ? 'Prod' : e === 'dev' ? 'Dev' : e);

export function deployedLabel(kind: DeployKind | string): string {
	switch (kind) {
		case 'promote':
			return 'Promoted';
		case 'rollback':
			return 'Rolled back';
		default:
			return 'Deployed';
	}
}

export const shortSha = (sha: string): string => sha.slice(0, 7);

export const actorLabel = (actor: string): string => (actor === 'homelab' ? 'Homelab' : actor);

export function isProblemAction(action: string): boolean {
	return /\.(failed|unhealthy)$/.test(action);
}

export function appInitial(name: string): string {
	return (name.trim()[0] ?? '?').toUpperCase();
}
