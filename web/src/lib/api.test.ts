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

describe('secrets API', () => {
	afterEach(() => vi.unstubAllGlobals());
	const json = (body: string, status = 200) => new Response(body, { status, headers: { 'Content-Type': 'application/json' } });

	it('lists secrets without values and says whether this person may ask for them', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json('{"envs":[{"env":"prod","secrets":null}]}')));
		await expect(api.appSecrets('movies')).resolves.toEqual({ envs: [{ env: 'prod', secrets: [] }], canSeeValues: false });
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json('{"envs":null,"canSeeValues":true}')));
		await expect(api.appSecrets('movies')).resolves.toEqual({ envs: [], canSeeValues: true });
	});

	it('asks the server for one value at a time', async () => {
		const fetch = vi.fn().mockImplementation(async () => json('{"value":"abc"}'));
		vi.stubGlobal('fetch', fetch);
		await expect(api.revealSecret('movies', 'prod', 'TMDB_API_KEY')).resolves.toBe('abc');
		expect(fetch.mock.calls[0][0]).toBe('/api/apps/movies/envs/prod/secrets/TMDB_API_KEY/reveal');
		expect(fetch.mock.calls[0][1].method).toBe('POST');
		await expect(api.revealSetting('GITHUB_TOKEN')).resolves.toBe('abc');
		expect(fetch.mock.calls[1][0]).toBe('/api/settings/secrets/GITHUB_TOKEN/reveal');
	});

	it('lists versions and restores one', async () => {
		const fetch = vi.fn().mockResolvedValueOnce(json('{"versions":[{"version":2,"setBy":"a","setAt":"2026-01-01T00:00:00Z"}]}')).mockImplementation(async () => new Response(null, { status: 204 }));
		vi.stubGlobal('fetch', fetch);
		await expect(api.secretVersions('movies', 'dev', 'A')).resolves.toEqual([{ version: 2, setBy: 'a', setAt: '2026-01-01T00:00:00Z' }]);
		expect(fetch.mock.calls[0][0]).toBe('/api/apps/movies/envs/dev/secrets/A/versions');
		await api.restoreSecret('movies', 'dev', 'A', 1);
		expect(fetch.mock.calls[1][0]).toBe('/api/apps/movies/envs/dev/secrets/A/versions/1/restore');
		await api.restoreSetting('GITHUB_TOKEN', 3);
		expect(fetch.mock.calls[2][0]).toBe('/api/settings/secrets/GITHUB_TOKEN/versions/3/restore');
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

describe('deleting a folder', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('sends DELETE with the CSRF header and an encoded name', async () => {
		const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
		vi.stubGlobal('fetch', fetch);
		await expect(api.deleteFileFolder('my folder')).resolves.toBeUndefined();
		expect(fetch).toHaveBeenCalledWith('/api/files/folders/my%20folder', {
			method: 'DELETE',
			headers: { Accept: 'application/json', 'X-Homelab-Request': '1' },
			body: undefined
		});
	});

	it('shows the server text when the folder still has files', async () => {
		const fetch = vi.fn().mockResolvedValue(new Response('{"error":"The folder still has files. Delete them first."}', { status: 409 }));
		vi.stubGlobal('fetch', fetch);
		await expect(api.deleteFileFolder('d')).rejects.toMatchObject({ message: 'The folder still has files. Delete them first.', status: 409 });
	});
});
