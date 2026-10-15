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

	it('requests a fresh initial-owner passkey link from the local setup screen', async () => {
		const fetch = vi.fn().mockResolvedValue(new Response('{"url":"http://localhost:7081/lc/fresh-token"}', {
			status: 201,
			headers: { 'Content-Type': 'application/json' }
		}));
		vi.stubGlobal('fetch', fetch);

		await expect(api.initialOwnerPasskeyLink()).resolves.toEqual({ url: 'http://localhost:7081/lc/fresh-token' });
		expect(fetch).toHaveBeenCalledWith('/api/setup/passkey-link', {
			method: 'POST',
			headers: { Accept: 'application/json', 'X-Homelab-Request': '1', 'Content-Type': 'application/json' },
			body: '{}'
		});
	});
});

describe('invite redemption API', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('returns the same Pocket ID registration link flow to an invited person', async () => {
		const fetch = vi.fn().mockResolvedValue(new Response('{"url":"http://localhost:7081/lc/long-code"}', {
			status: 201,
			headers: { 'Content-Type': 'application/json' }
		}));
		vi.stubGlobal('fetch', fetch);

		await expect(api.redeemInvite('invite-token')).resolves.toEqual({ url: 'http://localhost:7081/lc/long-code' });
		expect(fetch).toHaveBeenCalledWith('/api/invites/invite-token/redeem', {
			method: 'POST',
			headers: { Accept: 'application/json', 'X-Homelab-Request': '1', 'Content-Type': 'application/json' },
			body: '{}'
		});
	});
});
