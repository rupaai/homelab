import { describe, expect, it } from 'vitest';
import { buildScopes, endOfDay, keyProblem, scopeWords } from './keys';

describe('scopeWords', () => {
	it('puts each scope in plain words', () => {
		expect(scopeWords(['users:read'])).toEqual(['Read people']);
		expect(scopeWords(['files:photos:write'])).toEqual(['Write files in photos']);
		expect(scopeWords(['db:movies:read'])).toEqual(['Read the movies database']);
		expect(scopeWords(['db:movies:write'])).toEqual(['Write to the movies database']);
	});
	it('joins read and write on the same thing', () => {
		expect(scopeWords(['data:movies:read', 'data:movies:write'])).toEqual(['Read and write movies data']);
	});
	it('keeps unknown scopes as they are', () => expect(scopeWords(['odd'])).toEqual(['odd']));
});

describe('buildScopes', () => {
	it('builds scopes from the form choices', () => {
		expect(buildScopes(true, [{ kind: 'files', target: 'photos', access: 'both' }])).toEqual(['users:read', 'files:photos:read', 'files:photos:write']);
		expect(buildScopes(false, [{ kind: 'db', target: 'movies', access: 'write' }])).toEqual(['db:movies:write']);
	});
	it('drops repeats', () => {
		const g = { kind: 'data' as const, target: 'a', access: 'read' as const };
		expect(buildScopes(false, [g, g])).toEqual(['data:a:read']);
	});
});

describe('keyProblem', () => {
	it('asks for a name, a choice and good names', () => {
		expect(keyProblem(' ', true, [])).toMatch(/name/);
		expect(keyProblem('x', false, [])).toMatch(/at least one/);
		expect(keyProblem('x', false, [{ kind: 'files', target: 'My Photos', access: 'read' }])).toMatch(/lowercase/);
		expect(keyProblem('x', false, [{ kind: 'db', target: '', access: 'read' }])).toMatch(/app/);
		expect(keyProblem('x', true, [])).toBe('');
	});
});

describe('endOfDay', () => {
	it('is null without a date', () => expect(endOfDay('')).toBeNull());
	it('gives an ISO time', () => expect(endOfDay('2027-01-31')).toMatch(/^2027-0[12]-\d\dT/));
});
