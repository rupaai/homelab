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

describe('file upload API', () => {
	afterEach(() => vi.unstubAllGlobals());
	const file = new File(['hi'], 'a.txt', { type: 'text/plain' });
	const reply = (body: string, headers: Record<string, string> = {}) => vi.fn().mockResolvedValue(new Response(body, { status: 200, headers: { 'Content-Type': 'application/json', ...headers } }));

	it('says when the file was already in the folder', async () => {
		vi.stubGlobal('fetch', reply('{"id":"f1","folder":"d","name":"a.txt","tags":null,"meta":{"pages":2}}', { 'X-Homelab-Duplicate': 'true' }));
		const r = await api.uploadFile('d', file);
		expect(r.duplicate).toBe(true);
		expect(r.file.meta).toEqual({ pages: 2 });
		expect(r.file.tags).toEqual([]);
	});
	it('treats a new file as new and fills in missing meta', async () => {
		vi.stubGlobal('fetch', reply('{"id":"f1","folder":"d","name":"a.txt"}'));
		const r = await api.uploadFile('d', file);
		expect(r.duplicate).toBe(false);
		expect(r.file.meta).toEqual({});
	});
	it('sends the metadata filters', async () => {
		const fetch = reply('{"files":[{"id":"f1"}],"next":null}');
		vi.stubGlobal('fetch', fetch);
		const r = await api.files('d', { takenAfter: '2026-01-01', takenBefore: '2026-02-01', camera: 'iPhone 15' });
		expect(fetch.mock.calls[0][0]).toBe('/api/files/d?taken_after=2026-01-01&taken_before=2026-02-01&camera=iPhone+15');
		expect(r.files[0].meta).toEqual({});
	});
});
