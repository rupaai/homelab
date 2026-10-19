import type { KeyView } from './types';

export type GrantKind = 'files' | 'db' | 'data';
export type GrantAccess = 'read' | 'write' | 'both';
/** One line in the new-key form: what part of Homelab, which folder or app, and how much. */
export interface Grant { kind: GrantKind; target: string; access: GrantAccess; }

/** Folder and app names: lowercase letters, digits, - and _. */
export const validTarget = (s: string): boolean => /^[a-z0-9_-]+$/.test(s);

/** Words for one kind of access to one thing: "Read files in photos". */
function phrase(kind: string, target: string, access: string): string {
	const verb = access === 'both' ? 'Read and write' : access === 'write' ? 'Write' : 'Read';
	switch (kind) {
		case 'users':
			return `${verb} people`;
		case 'files':
			return `${verb} files in ${target}`;
		case 'db':
			return access === 'write' ? `Write to the ${target} database` : `${verb} the ${target} database`;
		case 'data':
			return `${verb} ${target} data`;
		default:
			return '';
	}
}

/** Scopes in plain words. Read and write on the same thing become one phrase. */
export function scopeWords(scopes: string[]): string[] {
	const order: string[] = [];
	const access = new Map<string, Set<string>>();
	for (const s of scopes) {
		const parts = s.split(':');
		const kind = parts[0];
		const level = parts[parts.length - 1];
		const target = parts.length === 3 ? parts[1] : '';
		if (!['users', 'files', 'db', 'data'].includes(kind) || !['read', 'write'].includes(level)) {
			order.push(s);
			continue;
		}
		const key = `${kind}:${target}`;
		if (!access.has(key)) {
			access.set(key, new Set());
			order.push(key);
		}
		access.get(key)!.add(level);
	}
	return order.map((key) => {
		const levels = access.get(key);
		if (!levels) return key;
		const [kind, target] = key.split(':');
		return phrase(kind, target, levels.size === 2 ? 'both' : [...levels][0]);
	});
}

/** The scope strings the server wants, for the form's choices. */
export function buildScopes(users: boolean, grants: Grant[]): string[] {
	const out: string[] = [];
	const add = (s: string) => out.includes(s) || out.push(s);
	if (users) add('users:read');
	for (const g of grants) {
		if (g.access !== 'write') add(`${g.kind}:${g.target}:read`);
		if (g.access !== 'read') add(`${g.kind}:${g.target}:write`);
	}
	return out;
}

/** Why the form can't be sent yet, in plain words, or '' when it can. */
export function keyProblem(name: string, users: boolean, grants: Grant[]): string {
	if (!name.trim()) return 'Give the key a name.';
	for (const g of grants) {
		if (!g.target) return g.kind === 'files' ? 'Type a folder name.' : 'Pick an app.';
		if (!validTarget(g.target)) return 'Folder names use lowercase letters, numbers, - and _.';
	}
	if (buildScopes(users, grants).length === 0) return 'Choose at least one thing the key can do.';
	return '';
}

/** A chosen end date (yyyy-mm-dd) becomes the end of that day, as the server wants it. */
export function endOfDay(date: string): string | null {
	if (!date) return null;
	const d = new Date(`${date}T23:59:59`);
	return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

export const keyOwner = (k: KeyView): string => k.createdByName || k.createdBy || 'Homelab';
