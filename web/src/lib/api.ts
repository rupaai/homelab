import { ApiError, NETWORK_MESSAGE, errorMessage } from './errors';
import { SIGN_IN_MESSAGE, sessionLost } from './session';
import { clearToken, getToken, setToken } from './token';
import type { Api, AppView, FileFolder, FilePage, FileQuery, StorageDisk, StoredFile, UploadResult, FileMeta, BootstrapView, CreatedKey, KeysView, DeviceView, EnvSecrets, Invitation, KeyView, MeView, ResourcesView, PasskeyLink, PersonInput, PersonView, PromotePreview, RemovalStep, RollbackPreview, SettingView, SetupResult, TestVersion } from './types';

/** Mock mode: `?mock` in the URL or VITE_MOCK=1 gives fake data, no server needed. Dev builds only, so the mock data is dropped from the real companion. */
export const useMock: boolean =
	import.meta.env.DEV && (import.meta.env.VITE_MOCK === '1' || (typeof location !== 'undefined' && new URLSearchParams(location.search).has('mock')));

/** The headers every call carries: the session token, and the CSRF header on anything that changes things. */
function authHeaders(change: boolean): Record<string, string> {
	const headers: Record<string, string> = {};
	const token = getToken();
	if (token) headers.Authorization = `Bearer ${token}`;
	if (change) headers['X-Homelab-Request'] = '1';
	return headers;
}

async function send(path: string, init: RequestInit): Promise<Response> {
	try {
		return await fetch(path, init);
	} catch {
		throw new ApiError(NETWORK_MESSAGE, 0);
	}
}

/** Turns a failed response into an ApiError. An expired session sends the layout back to sign-in. */
function failure(res: Response, text: string): ApiError {
	const err = new ApiError(errorMessage(res.status, text), res.status);
	if (res.status === 403 && err.message === SIGN_IN_MESSAGE) { clearToken(); sessionLost(); }
	return err;
}

async function request<T>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
	const headers: Record<string, string> = { Accept: 'application/json', ...authHeaders(method !== 'GET') };
	if (method !== 'GET' && (method !== 'DELETE' || body !== undefined)) headers['Content-Type'] = 'application/json';
	const res = await send(path, { method, headers, body: method === 'POST' || method === 'PUT' || body !== undefined ? JSON.stringify(body ?? {}) : undefined });
	const text = await res.text();
	if (!res.ok) throw failure(res, text);
	return (text ? JSON.parse(text) : undefined) as T;
}

/** The raw file as the body, not JSON. */
async function upload(path: string, file: File): Promise<UploadResult> {
	const headers = { Accept: 'application/json', 'Content-Type': file.type || 'application/octet-stream', ...authHeaders(true) };
	const res = await send(path, { method: 'POST', headers, body: file });
	const text = await res.text();
	if (!res.ok) throw failure(res, text);
	return { file: fixFile(JSON.parse(text) as StoredFile), duplicate: res.headers.get('X-Homelab-Duplicate') === 'true' };
}

/** A file's bytes. A plain <img src> or link can't send the token, so the screens fetch it here. */
async function download(path: string): Promise<Blob> {
	const res = await send(path, { method: 'GET', headers: authHeaders(false) });
	if (!res.ok) throw failure(res, await res.text());
	return res.blob();
}

const enc = encodeURIComponent;

// Go sends nil slices as null; the screens want plain arrays.
const list = <T>(v: T[] | null | undefined): T[] => v ?? [];

// Older servers may leave the milestone 2 fields out; fill them so the screens can rely on them.
function fixApp(a: AppView): AppView {
	return {
		...a,
		promoteAfter: a.promoteAfter ?? '',
		autoPromote: a.autoPromote ?? null,
		held: list(a.held),
		mirror: a.mirror ?? null,
		envs: list(a.envs).map((e) => ({ ...e, missingSecrets: list(e.missingSecrets) }))
	};
}

// Older servers send no meta; the screens want an object.
const fixFile = (f: StoredFile): StoredFile => ({ ...f, tags: list(f.tags), info: f.info ?? {}, meta: (f.meta ?? {}) as FileMeta });

const realApi: Api = {
	bootstrap: () => request<BootstrapView>('GET', '/api/bootstrap'),
	initialOwnerPasskeyLink: () => request<PasskeyLink>('POST', '/api/setup/passkey-link', {}),
	me: () => request<MeView>('GET', '/api/me'),
	passkeyLink: () => request<PasskeyLink>('POST', '/api/me/passkey-link'),
	setupOwner: (name, email) => request<SetupResult>('POST', '/api/setup', { name, email }),
	recover: async (code) => { setToken((await request<{ token: string }>('POST', '/api/recover', { code })).token); },
	passwordSignIn: async (password) => { setToken((await request<{ token: string }>('POST', '/api/password-sign-in', { password })).token); },
	signOut: async () => { await request<void>('POST', '/api/logout'); clearToken(); },
	people: async () => list(await request<PersonView[]>('GET', '/api/people')),
	invitePerson: (person) => request<Invitation>('POST', '/api/people', person),
	redeemInvite: (token) => request<PasskeyLink>('POST', `/api/invites/${enc(token)}/redeem`),
	resendInvite: (id) => request<{ url: string }>('POST', `/api/people/${enc(id)}/invite`),
	updatePerson: (id, person) => request<PersonView>('PUT', `/api/people/${enc(id)}`, person),
	removePerson: (id) => request<RemovalStep[]>('DELETE', `/api/people/${enc(id)}`),
	apps: async () => list(await request<AppView[]>('GET', '/api/apps')).map(fixApp),
	projects: async () => list(await request<AppView[]>('GET', '/api/projects')).map(fixApp),
	addApp: async (repo, branch) => fixApp(await request<AppView>('POST', '/api/apps', { repo, branch })),
	deploy: (id, commit) => request('POST', `/api/apps/${enc(id)}/deploy`, commit ? { commit } : {}),
	promotePreview: async (id) => {
		const p = await request<PromotePreview>('GET', `/api/apps/${enc(id)}/promote`);
		return { ...p, commits: list(p.commits), blockers: list(p.blockers), missingSecrets: list(p.missingSecrets) };
	},
	promote: (id) => request('POST', `/api/apps/${enc(id)}/promote`),
	rollbackPreview: async (id, env) => {
		const p = await request<RollbackPreview>('GET', `/api/apps/${enc(id)}/envs/${env}/rollback`);
		return { ...p, blockers: list(p.blockers) };
	},
	rollback: (id, env) => request('POST', `/api/apps/${enc(id)}/envs/${env}/rollback`),
	logs: async (id, env, lines) => (await request<{ lines: string }>('GET', `/api/apps/${enc(id)}/envs/${env}/logs?lines=${lines}`)).lines ?? '',
	pushKey: async () => (await request<{ publicKey: string }>('GET', '/api/push/key')).publicKey ?? '',
	subscribePush: (sub) => request('POST', '/api/push/subscriptions', sub),
	unsubscribePush: (endpoint) => request('DELETE', '/api/push/subscriptions', { endpoint }),
	testPush: async () => (await request<{ sent: number }>('POST', '/api/push/test')).sent,
	resources: async () => {
		const r = await request<ResourcesView>('GET', '/api/resources');
		return { ...r, apps: list(r.apps), problems: list(r.problems) };
	},
	device: async () => {
		const d = await request<DeviceView>('GET', '/api/device');
		return { ...d, problems: list(d.problems), system: d.system ? { ...d.system, disks: list(d.system.disks) } : undefined };
	},
	updateHomelab: async () => (await request<{ version: string }>('POST', '/api/device/update')).version,
	testVersions: async () => list(await request<TestVersion[]>('GET', '/api/device/tests')),
	tryTestVersion: (version) => request<TestVersion>('POST', '/api/device/test', { version }),
	endTest: async (keep) => (await request<{ version: string }>('POST', keep ? '/api/device/test/keep' : '/api/device/test/back')).version,
	activity: async (q = {}) => {
		const p = new URLSearchParams();
		if (q.app) p.set('app', q.app);
		if (q.before) p.set('before', String(q.before));
		if (q.limit) p.set('limit', String(q.limit));
		return list(await request('GET', `/api/activity?${p}`));
	},
	setPromotion: async (id, mode) => fixApp(await request<AppView>('PUT', `/api/apps/${enc(id)}/promotion`, { mode })),
	removeApp: (id) => request('DELETE', `/api/apps/${enc(id)}`),
	appSecrets: async (id) => {
		const r = await request<{ envs: EnvSecrets[] | null }>('GET', `/api/apps/${enc(id)}/secrets`);
		return list(r.envs).map((e) => ({ ...e, secrets: list(e.secrets) }));
	},
	setSecret: (id, env, name, value) => request('PUT', `/api/apps/${enc(id)}/envs/${env}/secrets/${enc(name)}`, { value }),
	deleteSecret: (id, env, name) => request('DELETE', `/api/apps/${enc(id)}/envs/${env}/secrets/${enc(name)}`),
	regenerateSecret: (id, env, name) => request('POST', `/api/apps/${enc(id)}/envs/${env}/secrets/${enc(name)}/regenerate`),
	settings: async () => list(await request<SettingView[]>('GET', '/api/settings/secrets')),
	setSetting: (name, value) => request('PUT', `/api/settings/secrets/${enc(name)}`, { value }),
	deleteSetting: (name) => request('DELETE', `/api/settings/secrets/${enc(name)}`),
	keys: async () => {
		const r = await request<{ admin: KeyView[] | null; app: KeyView[] | null }>('GET', '/api/keys');
		const fix = (k: KeyView): KeyView => ({ ...k, scopes: list(k.scopes) });
		return { admin: list(r.admin).map(fix), app: list(r.app).map(fix) };
	},
	createKey: async (input) => {
		const r = await request<CreatedKey>('POST', '/api/keys', input);
		return { ...r, info: { ...r.info, scopes: list(r.info.scopes) } };
	},
	revokeKey: (id) => request('DELETE', `/api/keys/${enc(id)}`),
	storage: async () => list(await request<StorageDisk[]>('GET', '/api/storage')),
	fileFolders: async () => list((await request<{ folders: FileFolder[] | null }>('GET', '/api/files/folders')).folders),
	createFileFolder: (name, disk) => request<FileFolder>('POST', '/api/files/folders', disk ? { name, disk } : { name }),
	deleteFileFolder: (name) => request('DELETE', `/api/files/folders/${enc(name)}`),
	files: async (folder, q = {}) => {
		const p = new URLSearchParams();
		if (q.tag) p.set('tag', q.tag);
		if (q.type) p.set('type', q.type);
		if (q.after) p.set('after', q.after);
		if (q.limit) p.set('limit', String(q.limit));
		if (q.takenAfter) p.set('taken_after', q.takenAfter);
		if (q.takenBefore) p.set('taken_before', q.takenBefore);
		if (q.camera) p.set('camera', q.camera);
		const r = await request<FilePage>('GET', `/api/files/${enc(folder)}?${p}`);
		return { files: list(r.files).map(fixFile), next: r.next ?? null };
	},
	uploadFile: (folder, file, tags = []) => {
		const p = new URLSearchParams({ name: file.name });
		if (tags.length) p.set('tags', tags.join(','));
		return upload(`/api/files/${enc(folder)}?${p}`, file);
	},
	fileBlob: (folder, id) => download(`/api/files/${enc(folder)}/${enc(id)}`),
	deleteFile: (folder, id) => request('DELETE', `/api/files/${enc(folder)}/${enc(id)}`)
};

// The import sits inside the DEV check, so production builds leave mock.ts out.
export const api: Api = import.meta.env.DEV && useMock ? (await import('./mock')).mockApi : realApi;
