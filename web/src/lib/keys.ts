import type { KeyView } from './types';

/** What a key can be allowed to do, in plain words. Databases and data come later. */
export const KEY_SCOPES: { scope: string; words: string }[] = [
	{ scope: 'users:read', words: 'Read people' },
	{ scope: 'files:read', words: 'Read files' },
	{ scope: 'files:write', words: 'Save and delete files' }
];

/** Scopes in plain words. Unknown ones stay as they are. */
export const scopeWords = (scopes: string[]): string[] => scopes.map((s) => KEY_SCOPES.find((k) => k.scope === s)?.words ?? s);

/** Why the form can't be sent yet, in plain words, or '' when it can. */
export const keyProblem = (name: string, scopes: string[] = ['users:read']): string =>
	!name.trim() ? 'Give the key a name.' : scopes.length ? '' : 'Choose what the key may do.';

/** A chosen end date (yyyy-mm-dd) becomes the end of that day, as the server wants it. */
export function endOfDay(date: string): string | null {
	if (!date) return null;
	const d = new Date(`${date}T23:59:59`);
	return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

export const keyOwner = (k: KeyView): string => k.createdByName || k.createdBy || 'Homelab';
