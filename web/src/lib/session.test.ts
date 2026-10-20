import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { onSessionLost } from './session';

const reply = (status: number, error: string) =>
	vi.fn().mockResolvedValue(new Response(JSON.stringify({ error }), { status, headers: { 'Content-Type': 'application/json' } }));

describe('session lost', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('tells the layout when the API says to sign in', async () => {
		vi.stubGlobal('fetch', reply(403, 'Sign in to use Homelab.'));
		const lost = vi.fn();
		const stop = onSessionLost(lost);
		await expect(api.apps()).rejects.toThrow('Sign in to use Homelab.');
		expect(lost).toHaveBeenCalledTimes(1);
		stop();
	});

	it('ignores other 403s', async () => {
		vi.stubGlobal('fetch', reply(403, "You don't have access to that part of Homelab."));
		const lost = vi.fn();
		const stop = onSessionLost(lost);
		await expect(api.apps()).rejects.toThrow();
		expect(lost).not.toHaveBeenCalled();
		stop();
	});
});
