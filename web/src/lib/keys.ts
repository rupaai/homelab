import type { KeyView } from './types';

/** The only thing a key can do so far. Files, databases and data come later. */
export const KEY_SCOPES = ['users:read'];

/** Scopes in plain words. Unknown ones stay as they are. */
export const scopeWords = (scopes: string[]): string[] => scopes.map((s) => (s === 'users:read' ? 'Read people' : s));

/** Why the form can't be sent yet, in plain words, or '' when it can. */
export const keyProblem = (name: string): string => (name.trim() ? '' : 'Give the key a name.');

/** A chosen end date (yyyy-mm-dd) becomes the end of that day, as the server wants it. */
export function endOfDay(date: string): string | null {
	if (!date) return null;
	const d = new Date(`${date}T23:59:59`);
	return Number.isNaN(d.getTime()) ? null : d.toISOString();
}

export const keyOwner = (k: KeyView): string => k.createdByName || k.createdBy || 'Homelab';
