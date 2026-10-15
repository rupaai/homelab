import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';

describe('passkey link API', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('requests a fresh link using the signed-in self endpoint', async () => {
		const fetch = vi.fn().mockResolvedValue(new Response('{"url":"http://localhost:7081/setup"}', {
			status: 201,
			headers: { 'Content-Type': 'application/json' }
		}));
		vi.stubGlobal('fetch', fetch);

		await expect(api.passkeyLink()).resolves.toEqual({ url: 'http://localhost:7081/setup' });
		expect(fetch).toHaveBeenCalledWith('/api/me/passkey-link', {
			method: 'POST',
			headers: { Accept: 'application/json', 'X-Homelab-Request': '1', 'Content-Type': 'application/json' },
			body: '{}'
		});
	});
});
