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

/** bytes as people read them: 182 MB, 5.1 GB. */
export function bytes(n: number): string {
	if (!Number.isFinite(n) || n < 0) return '–';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	let i = 0;
	while (n >= 1000 && i < units.length - 1) { n /= 1000; i++; }
	return `${n >= 10 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`;
}

/** how long something has been up: "9 days", "3 hours", "12 minutes". */
export function uptime(seconds: number | undefined): string {
	if (!seconds || seconds < 0) return '–';
	const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? '' : 's'}`;
	if (seconds >= 86400) return plural(Math.floor(seconds / 86400), 'day');
	if (seconds >= 3600) return plural(Math.floor(seconds / 3600), 'hour');
	return plural(Math.max(1, Math.floor(seconds / 60)), 'minute');
}

/** An app's address as this device can open it. With addresses: ip Homelab reports apps on
 * localhost; opened from another device, the companion's own host name is the way in. */
export function appAddress(address: string, host = typeof location === 'undefined' ? 'localhost' : location.hostname): string {
	try {
		const u = new URL(address);
		if (u.hostname !== 'localhost' || host === 'localhost' || host === '127.0.0.1' || host === '') return address;
		u.hostname = host;
		return u.toString().replace(/\/$/, '');
	} catch { return address; }
}

/** "3 Oct 2026", or nothing when the text isn't a date. */
export function shortDate(iso: string | null | undefined): string {
	if (!iso) return '';
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

/** What people call a release: its version when it has one, else the short commit. */
export function releaseName(r: { version?: string; short: string } | null | undefined): string {
	return r ? r.version || r.short : '';
}

/** The title of the promote sheet: "Promote v0.4.0-dev.2 to prod as v0.4.0" when both versions are known. */
export function promoteTitle(devVersion: string | undefined, nextVersion: string | undefined): string {
	if (devVersion && nextVersion) return `Promote ${devVersion} to prod as ${nextVersion}`;
	if (nextVersion) return `Promote to prod as ${nextVersion}`;
	return 'Promote to prod';
}

/**
 * Release notes (markdown with "## New" headings and "- item" lines) as a plain list. Items
 * under "Breaking" start with "Breaking:" so they stand out without the headings.
 */
export function noteLines(notes: string | undefined): string[] {
	const out: string[] = [];
	let breaking = false;
	for (const raw of (notes ?? '').split('\n')) {
		const line = raw.trim();
		const heading = /^#{1,6}\s+(.*)$/.exec(line);
		if (heading) {
			breaking = heading[1].trim().toLowerCase() === 'breaking';
		} else if (line.startsWith('- ') && line.length > 2) {
			out.push(breaking ? `Breaking: ${line.slice(2)}` : line.slice(2));
		}
	}
	return out;
}

/** "1 breaking, 2 new, 1 fix" for the changes of a Homelab release; nothing when it has none. */
export function changesSummary(c: { breaking: number; new: number; fixes: number } | undefined): string {
	if (!c) return '';
	const parts: string[] = [];
	if (c.breaking > 0) parts.push(`${c.breaking} breaking`);
	if (c.new > 0) parts.push(`${c.new} new`);
	if (c.fixes > 0) parts.push(c.fixes === 1 ? '1 fix' : `${c.fixes} fixes`);
	return parts.join(', ');
}

/** Release notes ("## New" then "- line" lines) as sections to show as lists. */
export function noteSections(notes: string | undefined): { title: string; items: string[] }[] {
	const out: { title: string; items: string[] }[] = [];
	for (const raw of (notes ?? '').split('\n')) {
		const line = raw.trim();
		if (line.startsWith('## ')) out.push({ title: line.slice(3), items: [] });
		else if (line.startsWith('- ') && out.length > 0) out[out.length - 1].items.push(line.slice(2));
	}
	return out;
}
