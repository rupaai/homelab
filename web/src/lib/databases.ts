import type { DbInfo } from './types';

/** How many rows the table view shows at a time. */
export const PAGE_ROWS = 50;

/** The database's state in plain words. */
export function dbStatusWords(status: DbInfo['status']): string {
	if (status === 'ok') return 'Working';
	if (status === 'empty') return 'Nothing saved yet';
	return 'Not answering';
}

export const tableCount = (n: number): string => `${n} ${n === 1 ? 'table' : 'tables'}`;

/** What a kind of copy means, in plain words. Unknown kinds stay as they are. */
export function copyKindWords(kind: string): string {
	switch (kind) {
		case 'manual': return 'Made by hand';
		case 'promote': return 'Before a promote';
		case 'before-restore': return 'Before a restore';
		case 'before-copy-from-prod': return 'Before copying prod';
		default: return kind;
	}
}

/** Changing prod's data asks first. */
export const needsConfirm = (env: string, write: boolean): boolean => write && env === 'prod';

/** The tunnel for the Mac: local port 15432 goes to the database on the device. */
export const tunnelCommand = (port: number, login: string, host: string): string => `ssh -N -L 15432:127.0.0.1:${port} ${login.trim()}@${host}`;

/** The address to paste into a database tool, once the tunnel is open. */
export const dbAddress = (password: string): string => `postgres://app:${password}@127.0.0.1:15432/app`;

/** Only plain login names, so the command can't carry anything else. */
export const loginProblem = (login: string): string => (/^[A-Za-z0-9._-]+$/.test(login.trim()) ? '' : 'Type your login on this device, like alex.');

const LOGIN_KEY = 'homelab.db.login';

/** The login saved on this phone or computer. Storage can be blocked, so this never throws. */
export function savedLogin(): string {
	try {
		return localStorage.getItem(LOGIN_KEY) ?? '';
	} catch {
		return '';
	}
}

export function saveLogin(login: string): void {
	try {
		localStorage.setItem(LOGIN_KEY, login.trim());
	} catch {
		// Not saved. It still works for now.
	}
}

/** "Rows 1 to 50", "Rows 51 to 90". */
export const rowsRange = (offset: number, count: number): string => (count === 0 ? 'No rows' : `Rows ${offset + 1} to ${offset + count}`);
