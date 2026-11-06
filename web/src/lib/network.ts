import type { PortRow } from './types';

export const HOME = 'home network';

/** Is the port open to other devices on the home network? */
export const isOpen = (r: Pick<PortRow, 'reach'>): boolean => r.reach === HOME;

/** "8096 tcp" or "6881-6891 udp". */
export const portText = (r: Pick<PortRow, 'hostPort' | 'protocol'>): string => `${r.hostPort} ${r.protocol}`;

/** What the row is for, in plain words. */
export function serviceWords(r: Pick<PortRow, 'app' | 'service'>): string {
	if (r.app === 'homelab') return r.service === 'companion' ? 'Companion' : r.service === 'app door' ? 'App door' : r.service === 'services' ? 'Platform services' : r.service;
	switch (r.service) {
		case 'app':
			return 'The app';
		case 'db':
			return 'Database';
		case 'address':
			return 'Web address';
		default:
			return r.service;
	}
}

/** Who can reach it, in words that don't need colour. */
export const reachWords = (r: Pick<PortRow, 'reach'>): string => (isOpen(r) ? 'Home network' : 'This device only');

export interface PortGroup {
	key: string;
	title: string;
	rows: PortRow[];
}

/** Rows grouped as Homelab first, then each app and environment in the order the server sent them. */
export function groupPorts(rows: PortRow[]): PortGroup[] {
	const groups: PortGroup[] = [];
	for (const r of rows) {
		const key = r.env ? `${r.app}/${r.env}` : r.app;
		let g = groups.find((x) => x.key === key);
		if (!g) groups.push((g = { key, title: r.env ? `${r.appName} · ${r.env}` : r.appName, rows: [] }));
		g.rows.push(r);
	}
	return groups.sort((a, b) => Number(b.key === 'homelab') - Number(a.key === 'homelab'));
}

/** "3 ports are open to the home network." */
export function openSummary(rows: PortRow[]): string {
	const n = rows.filter(isOpen).length;
	if (n === 0) return 'Nothing is open to the home network.';
	return `${n} ${n === 1 ? 'port is' : 'ports are'} open to the home network.`;
}
