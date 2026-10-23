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

/** Upload progress as a whole percent, 0 to 100. */
export const percent = (done: number): string => `${Math.round(Math.min(1, Math.max(0, done)) * 100)}%`;

/** The line under the Allow changes switch. */
export const writeHint = (write: boolean): string => (write ? 'This can change data.' : 'Read only. Nothing is changed.');

/** On prod, a copy is made before a change. */
export const PROD_COPY_NOTE = 'On prod, Homelab makes a copy first, so you can undo with Restore.';

/** The word to type before a risky change goes through. */
export const confirmLabel = (env: string): string => `Type ${env} to run it`;

/** A person's name from their id; the id itself when the list does not have them. */
export function personName(people: { id: string; name: string }[], id: string): string {
	return people.find((p) => p.id === id)?.name || id;
}

/** A change's time as an ISO string, for the Time component. Changes carry unix seconds. */
export const changeTime = (at: number): string => new Date(at * 1000).toISOString();

/** A change's result in words. */
export const changeWords = (ok: boolean, rows: number): string => (ok ? `Worked · ${rows} ${rows === 1 ? 'row' : 'rows'}` : 'Failed');

/** A change that has more behind it when the last page came back full. */
export const hasMoreChanges = (got: number, limit: number): boolean => got >= limit;
