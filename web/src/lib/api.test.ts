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
		await expect(api.appSecrets('movies')).resolves.toEqual({ envs: [{ env: 'prod', secrets: [], saved: [] }], canSeeValues: false });
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

	it('lists what the app saved and shows or deletes one', async () => {
		const fetch = vi.fn().mockImplementation(async (_u: string, init: RequestInit) => (init.method === 'DELETE' ? new Response(null, { status: 204 }) : json('{"value":"tok"}')));
		vi.stubGlobal('fetch', fetch);
		await expect(api.revealSavedSecret('movies', 'prod', 'google:u 1')).resolves.toBe('tok');
		expect(fetch.mock.calls[0][0]).toBe('/api/apps/movies/envs/prod/saved/google%3Au%201/reveal');
		expect(fetch.mock.calls[0][1].method).toBe('POST');
		await api.deleteSavedSecret('movies', 'prod', 'x');
		expect(fetch.mock.calls[1][0]).toBe('/api/apps/movies/envs/prod/saved/x');
		expect(fetch.mock.calls[1][1].method).toBe('DELETE');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json('{"envs":[{"env":"prod","secrets":[],"saved":[{"name":"a","version":2,"updatedAt":"2026-01-01T00:00:00Z"}]}]}')));
		await expect(api.appSecrets('movies')).resolves.toMatchObject({ envs: [{ saved: [{ name: 'a', version: 2 }] }] });
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

describe('invite and password API', () => {
	afterEach(() => vi.unstubAllGlobals());
	const json = (body: string, status = 200) => new Response(body, { status, headers: { 'Content-Type': 'application/json' } });
	const store = new Map<string, string>();
	const stubStorage = () => vi.stubGlobal('localStorage', { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v), removeItem: (k: string) => void store.delete(k) });

	it('sends the new password to the invite and keeps the session it gets back', async () => {
		stubStorage();
		const fetch = vi.fn().mockResolvedValue(json('{"token":"hls_new"}'));
		vi.stubGlobal('fetch', fetch);
		await api.setInvitePassword('invite-token', 'a long password');
		expect(fetch.mock.calls[0][0]).toBe('/api/invites/invite-token/password');
		expect(fetch.mock.calls[0][1].body).toBe('{"password":"a long password"}');
		expect(store.get('homelab_session')).toBe('hls_new');
	});

	it('sends the email only when there is one', async () => {
		stubStorage();
		const fetch = vi.fn().mockImplementation(async () => json('{"token":"hls_x"}'));
		vi.stubGlobal('fetch', fetch);
		await api.passwordSignIn('pw');
		await api.passwordSignIn('pw', 'sam@example.test');
		expect(fetch.mock.calls[0][1].body).toBe('{"password":"pw"}');
		expect(fetch.mock.calls[1][1].body).toBe('{"email":"sam@example.test","password":"pw"}');
	});

	it('reads whether the invite was emailed', async () => {
		const fetch = vi.fn().mockResolvedValue(json('{"id":"p1","url":"http://192.168.1.183:7080/invite/t","emailed":false,"emailError":"Email isn\'t set up, so send the link yourself."}', 201));
		vi.stubGlobal('fetch', fetch);
		await expect(api.invitePerson({ name: 'Sam', email: 'sam@example.test', roles: ['member'], grants: {} })).resolves.toMatchObject({ emailed: false, emailError: expect.stringContaining('send the link yourself') });
	});

	it('asks for a test email', async () => {
		const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
		vi.stubGlobal('fetch', fetch);
		await api.testEmail();
		expect(fetch.mock.calls[0][0]).toBe('/api/email/test');
	});
});

describe('databases API', () => {
	afterEach(() => vi.unstubAllGlobals());
	const json = (body: string, status = 200) => new Response(body, { status, headers: { 'Content-Type': 'application/json' } });

	it('lists databases, with null as none', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json('{"databases":null}')));
		await expect(api.databases()).resolves.toEqual([]);
	});

	it('asks for 50 rows of a table at an offset, with the name encoded', async () => {
		const fetch = vi.fn().mockResolvedValue(json('{"columns":["id"],"rows":[["1"]],"truncated":false}'));
		vi.stubGlobal('fetch', fetch);
		await expect(api.dbRows('movies', 'dev', { schema: 'public', name: 'my notes', rows: 1 }, 50)).resolves.toEqual({ columns: ['id'], rows: [['1']], truncated: false });
		expect(fetch.mock.calls[0][0]).toBe('/api/db/movies/dev/tables/my%20notes/rows?schema=public&limit=50&offset=50');
	});

	it('sends the SQL with write off or on', async () => {
		const fetch = vi.fn().mockImplementation(async () => json('{"columns":null,"rows":null,"row_count":0,"truncated":false,"command":"UPDATE 0","ms":3}'));
		vi.stubGlobal('fetch', fetch);
		const r = await api.dbQuery('movies', 'prod', 'update t set a = 1', true);
		expect(r.columns).toEqual([]);
		expect(fetch.mock.calls[0][0]).toBe('/api/db/movies/prod/query');
		expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ sql: 'update t set a = 1', write: true });
	});

	it('reads copies and starts the copy actions', async () => {
		const fetch = vi.fn().mockImplementation(async (url: string) => (url.endsWith('/db') ? json('{"engine":"postgres","envs":[{"env":"dev","port":17005,"copies":null}]}') : json('', 202)));
		vi.stubGlobal('fetch', fetch);
		await expect(api.appDatabase('movies')).resolves.toEqual({ engine: 'postgres', envs: [{ env: 'dev', port: 17005, copies: [] }] });
		await api.makeDbCopy('movies', 'dev');
		await api.restoreDbCopy('movies', 'dev', '20261023-120000-manual');
		await api.copyProdToDev('movies');
		expect(fetch.mock.calls.slice(1).map((c) => `${c[1].method} ${c[0]}`)).toEqual([
			'POST /api/apps/movies/envs/dev/db/copies',
			'POST /api/apps/movies/envs/dev/db/copies/20261023-120000-manual/restore',
			'POST /api/apps/movies/db/copy-prod-to-dev'
		]);
	});

	it('sends the typed confirm only when there is one, and flags a needs_confirm answer', async () => {
		const fetch = vi.fn().mockResolvedValueOnce(json('{"error":"This can\'t be undone easily. Type prod to run it.","needs_confirm":true}', 409)).mockResolvedValueOnce(json('{"columns":[],"rows":[],"row_count":1,"truncated":false,"command":"DELETE 1","ms":3}'));
		vi.stubGlobal('fetch', fetch);
		const err = await api.dbQuery('movies', 'prod', 'delete from t', true).catch((e) => e);
		expect(err.status).toBe(409);
		expect(err.needsConfirm).toBe(true);
		expect(err.message).toBe("This can't be undone easily. Type prod to run it.");
		await api.dbQuery('movies', 'prod', 'delete from t', true, 'prod');
		expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ sql: 'delete from t', write: true, confirm: 'prod' });
	});

	it('does not flag other failures as needing a confirm', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json('{"error":"Leave out BEGIN and COMMIT; Homelab runs it all in one go."}', 400)));
		const err = await api.dbQuery('movies', 'prod', 'begin', true).catch((e) => e);
		expect(err.needsConfirm).toBe(false);
	});

	it('reads the changes list, paging with before', async () => {
		const fetch = vi.fn().mockImplementation(async () => json('{"changes":[{"id":9,"person":"p1","at":1790000000,"sql":"delete from t where a=1","ok":true,"rows":1,"ms":4,"error":"","copy":"c1"},{"id":8,"person":"p1","at":1789999999,"sql":"x","ok":false,"rows":0,"ms":1,"error":"bad","copy":null}]}'));
		vi.stubGlobal('fetch', fetch);
		const r = await api.dbChanges('movies', 'prod');
		expect(r.map((c) => c.copy)).toEqual(['c1', null]);
		expect(fetch.mock.calls[0][0]).toBe('/api/db/movies/prod/changes?limit=50');
		await api.dbChanges('movies', 'prod', 8, 10);
		expect(fetch.mock.calls[1][0]).toBe('/api/db/movies/prod/changes?limit=10&before=8');
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json('{"changes":null}')));
		await expect(api.dbChanges('movies', 'prod')).resolves.toEqual([]);
	});

	describe('importing a file', () => {
		class FakeXhr {
			static last: FakeXhr;
			static status = 202;
			static reply = '{"ok":true,"kind":"sqlite"}';
			headers: Record<string, string> = {};
			upload: { onprogress?: (e: { lengthComputable: boolean; loaded: number; total: number }) => void } = {};
			onload?: () => void;
			onerror?: () => void;
			onabort?: () => void;
			status = 0;
			responseText = '';
			method = '';
			url = '';
			body: unknown;
			constructor() { FakeXhr.last = this; }
			open(m: string, u: string) { this.method = m; this.url = u; }
			setRequestHeader(k: string, v: string) { this.headers[k] = v; }
			send(b: unknown) {
				this.body = b;
				this.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 4 });
				this.status = FakeXhr.status;
				this.responseText = FakeXhr.reply;
				this.onload?.();
			}
		}
		afterEach(() => { FakeXhr.status = 202; FakeXhr.reply = '{"ok":true,"kind":"sqlite"}'; });

		it('posts the raw file with progress and answers the kind', async () => {
			vi.stubGlobal('XMLHttpRequest', FakeXhr);
			const file = new File(['abcd'], 'my.db');
			const seen: number[] = [];
			await expect(api.importDb('movies', 'dev', file, (d) => seen.push(d))).resolves.toBe('sqlite');
			expect(FakeXhr.last.method).toBe('POST');
			expect(FakeXhr.last.url).toBe('/api/apps/movies/envs/dev/db/import');
			expect(FakeXhr.last.headers['Content-Type']).toBe('application/octet-stream');
			expect(FakeXhr.last.headers['X-Homelab-Request']).toBe('1');
			expect(FakeXhr.last.body).toBe(file);
			expect(seen).toEqual([0.25]);
		});

		it('turns a 400 or 409 into a plain error', async () => {
			vi.stubGlobal('XMLHttpRequest', FakeXhr);
			FakeXhr.status = 400;
			FakeXhr.reply = '{"error":"That file is empty."}';
			await expect(api.importDb('movies', 'dev', new File([''], 'a.db'))).rejects.toMatchObject({ message: 'That file is empty.', status: 400 });
			FakeXhr.status = 409;
			FakeXhr.reply = '{"error":"Homelab is busy with this app."}';
			await expect(api.importDb('movies', 'dev', new File(['x'], 'a.db'))).rejects.toMatchObject({ status: 409 });
		});
	});
});
