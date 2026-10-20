import { describe, expect, it } from 'vitest';
import { endOfDay, keyProblem, scopeWords } from './keys';

describe('scopeWords', () => {
	it('puts scopes in plain words', () => expect(scopeWords(['users:read'])).toEqual(['Read people']));
	it('keeps unknown scopes as they are', () => expect(scopeWords(['odd'])).toEqual(['odd']));
});

describe('keyProblem', () => {
	it('asks for a name', () => {
		expect(keyProblem(' ')).toMatch(/name/);
		expect(keyProblem('x')).toBe('');
	});
});

describe('endOfDay', () => {
	it('is null without a date', () => expect(endOfDay('')).toBeNull());
	it('gives an ISO time', () => expect(endOfDay('2027-01-31')).toMatch(/^2027-0[12]-\d\dT/));
});
