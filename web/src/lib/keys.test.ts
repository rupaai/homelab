import { describe, expect, it } from 'vitest';
import { endOfDay, keyProblem, scopeWords } from './keys';

describe('scopeWords', () => {
	it('puts scopes in plain words', () => expect(scopeWords(['users:read', 'files:write'])).toEqual(['Read people', 'Save and delete files']));
	it('keeps unknown scopes as they are', () => expect(scopeWords(['odd'])).toEqual(['odd']));
});

describe('keyProblem', () => {
	it('asks for a name', () => {
		expect(keyProblem(' ')).toMatch(/name/);
		expect(keyProblem('x')).toBe('');
		expect(keyProblem('x', [])).toMatch(/may do/);
	});
});

describe('endOfDay', () => {
	it('is null without a date', () => expect(endOfDay('')).toBeNull());
	it('gives an ISO time', () => expect(endOfDay('2027-01-31')).toMatch(/^2027-0[12]-\d\dT/));
});
