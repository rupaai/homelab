import type { LogCounts, LogLevel, LogLine, LogPage, LogQuery } from './types';

export interface LevelInfo {
	level: LogLevel;
	number: number;
	/** The three letters on every line. */
	tag: string;
	/** The word a screen reader says. */
	word: string;
	/** A colour name the screen turns into a style. */
	colour: 'grey' | 'blue' | 'green' | 'yellow' | 'red' | 'purple';
	/** One SVG path (24x24, stroke only). */
	icon: string;
}

/** Quiet to loud. Unknown comes first because it has no number worth judging. */
export const LEVELS: LevelInfo[] = [
	{ level: 'unknown', number: 0, tag: '---', word: 'Unknown level', colour: 'grey', icon: 'M9 9a3 3 0 1 1 4.5 2.6c-1 .6-1.5 1.1-1.5 2.4M12 17.5v.1' },
	{ level: 'debug', number: 1, tag: 'DBG', word: 'Debug', colour: 'blue', icon: 'M9 9h6v8a3 3 0 0 1-6 0zM9 12H5M15 12h4M9 16H5M15 16h4M10 9V7a2 2 0 1 1 4 0v2' },
	{ level: 'info', number: 2, tag: 'INF', word: 'Info', colour: 'green', icon: 'M12 11v6M12 7.5v.1M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18' },
	{ level: 'warn', number: 3, tag: 'WRN', word: 'Warning', colour: 'yellow', icon: 'M12 4 2.5 20h19zM12 10v4.5M12 17.5v.1' },
	{ level: 'error', number: 4, tag: 'ERR', word: 'Error', colour: 'red', icon: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18M9 9l6 6M15 9l-6 6' },
	{ level: 'fatal', number: 5, tag: 'FTL', word: 'Fatal', colour: 'purple', icon: 'M7.5 3h9l4.5 4.5v9L16.5 21h-9L3 16.5v-9zM12 8v5M12 16v.1' }
];

const UNKNOWN = LEVELS[0];

/** Info about a level. A level the server adds later shows as unknown. */
export const levelInfo = (level: string): LevelInfo => LEVELS.find((l) => l.level === level) ?? UNKNOWN;

/** The levels the screen offers as filters, loudest first. */
export const FILTER_LEVELS: LevelInfo[] = [...LEVELS].reverse();

export const noCounts = (): LogCounts => ({ debug: 0, info: 0, warn: 0, error: 0, fatal: 0, unknown: 0 });

export interface Range { id: string; label: string; ms: number; }
export const RANGES: Range[] = [
	{ id: '15m', label: 'Last 15 min', ms: 15 * 60_000 },
	{ id: '1h', label: 'Last hour', ms: 60 * 60_000 },
	{ id: '24h', label: 'Last 24 hours', ms: 24 * 60 * 60_000 },
	{ id: '7d', label: 'Last 7 days', ms: 7 * 24 * 60 * 60_000 },
	{ id: '14d', label: 'Last 14 days', ms: 14 * 24 * 60 * 60_000 }
];

/** The `since` time for a range id like "1h", or nothing for an unknown id. */
export function sinceFor(range: string, now: number = Date.now()): string | undefined {
	const r = RANGES.find((x) => x.id === range);
	return r ? new Date(now - r.ms).toISOString() : undefined;
}

/** The query string for GET /api/logs. Empty values are left out. */
export function logsParams(q: LogQuery): URLSearchParams {
	const p = new URLSearchParams();
	const set = (k: string, v: string | number | undefined) => {
		if (v !== undefined && v !== '' && !(typeof v === 'number' && !Number.isFinite(v))) p.set(k, String(v));
	};
	set('q', q.q?.trim());
	set('app', q.app);
	set('env', q.env);
	set('service', q.service);
	if (q.level?.length) p.set('level', q.level.join(','));
	set('min_level', q.minLevel);
	set('since', q.since);
	set('until', q.until);
	set('before', q.before);
	set('after', q.after);
	set('limit', q.limit);
	return p;
}

const text = (v: unknown): string => (typeof v === 'string' ? v : '');

/** Makes an answer safe to show: missing lists and counts become empty ones. */
export function fixLogPage(r: Partial<LogPage> | null | undefined): LogPage {
	const lines = (Array.isArray(r?.lines) ? r.lines : []).map((l): LogLine => ({
		id: Number(l.id),
		time: text(l.time),
		level: LEVELS.some((x) => x.level === l.level) ? l.level : 'unknown',
		msg: text(l.msg),
		app: text(l.app),
		env: text(l.env),
		service: text(l.service),
		stream: text(l.stream),
		extra: l.extra && typeof l.extra === 'object' && !Array.isArray(l.extra) ? l.extra : {}
	}));
	return { lines, counts: { ...noCounts(), ...(r?.counts ?? {}) }, next: r?.next ?? null };
}

/** Where a line came from: "homelab · files" for Homelab's own lines, "northern-lights · prod" for an app's. */
export function chipFor(line: Pick<LogLine, 'app' | 'env' | 'service'>): { text: string; homelab: boolean } {
	if (line.app === 'homelab') return { text: line.service ? `homelab · ${line.service}` : 'homelab', homelab: true };
	return { text: [line.app, line.env].filter(Boolean).join(' · ') || 'unknown', homelab: false };
}

export function addCounts(a: LogCounts, b: LogCounts): LogCounts {
	const out = noCounts();
	for (const l of LEVELS) out[l.level] = (a[l.level] ?? 0) + (b[l.level] ?? 0);
	return out;
}

/** New lines (newest first) go on top. A line already shown is not shown twice. */
export function mergeNewer(shown: LogLine[], incoming: LogLine[]): LogLine[] {
	const have = new Set(shown.map((l) => l.id));
	return [...incoming.filter((l) => !have.has(l.id)), ...shown].sort((x, y) => y.id - x.id);
}

/** The newest id on screen, for `after`. */
export const newestId = (lines: LogLine[]): number | undefined => lines.reduce<number | undefined>((m, l) => (m === undefined || l.id > m ? l.id : m), undefined);

/** "14:03:21" in the viewer's time. */
export function lineTime(iso: string): string {
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour12: false });
}

/** The extra fields as readable text, or nothing when there are none. */
export function extraText(extra: Record<string, unknown> | undefined): string {
	return extra && Object.keys(extra).length ? JSON.stringify(extra, null, 2) : '';
}

/** A screen address for an app's logs. */
export function logsHref(app: string, env?: string): string {
	const p = new URLSearchParams({ app });
	if (env) p.set('env', env);
	return `/logs?${p}`;
}
