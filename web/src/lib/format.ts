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

/** "in 12 min" / "in 2 hours" / "any moment now" for a future time (the page clock ticks every 30 s). */
export function countdown(iso: string | null | undefined, now: number = Date.now()): string {
	if (!iso) return '';
	const t = Date.parse(iso);
	if (Number.isNaN(t)) return '';
	const sec = Math.round((t - now) / 1000);
	if (sec < 45) return 'any moment now';
	const min = Math.round(sec / 60);
	if (min < 60) return `in ${min} min`;
	const hr = Math.round(min / 60);
	if (hr < 48) return `in ${hr} ${hr === 1 ? 'hour' : 'hours'}`;
	return `in ${Math.round(hr / 24)} days`;
}

export function promotionLabel(mode: string): string {
	switch (mode) {
		case 'manual':
			return 'Manual';
		case 'auto':
			return 'Fully automatic';
		default:
			return 'New builds to dev';
	}
}

/** One line on what each promotion mode does. */
export function promotionHelp(mode: string, promoteAfter: string): string {
	switch (mode) {
		case 'manual':
			return 'Nothing happens by itself. You choose when a build goes to dev and to prod.';
		case 'auto':
			return `Promotes to prod after dev has been healthy for ${promoteAfter || 'a while'} and its smoke test passes.`;
		default:
			return 'New builds that pass go to dev by themselves. You promote to prod by hand.';
	}
}

export function autoStateLabel(state: string): string {
	switch (state) {
		case 'waiting':
			return 'Waiting';
		case 'smoke-testing':
			return 'Testing';
		case 'held':
			return 'Held back';
		case 'blocked':
			return 'Blocked';
		case 'done':
			return 'Up to date';
		default:
			return state;
	}
}
