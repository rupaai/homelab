import { ApiError, NETWORK_MESSAGE, errorMessage } from './errors';
import { mockApi } from './mock';
import type { Api, AppView, BootstrapView, DeviceView, EnvSecrets, Invitation, MeView, ResourcesView, PasskeyLink, PersonInput, PersonView, PromotePreview, RemovalStep, RollbackPreview, SettingView, SetupResult, TestVersion } from './types';

/** Mock mode: `?mock` in the URL or VITE_MOCK=1 gives fake data, no server needed. */
export const useMock: boolean =
	import.meta.env.VITE_MOCK === '1' || (typeof location !== 'undefined' && new URLSearchParams(location.search).has('mock'));

async function request<T>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown): Promise<T> {
	const headers: Record<string, string> = { Accept: 'application/json' };
	if (method !== 'GET') {
		headers['X-Homelab-Request'] = '1';
		if (method !== 'DELETE' || body !== undefined) headers['Content-Type'] = 'application/json';
	}
	let res: Response;
	try {
		res = await fetch(path, { method, headers, body: method === 'POST' || method === 'PUT' || body !== undefined ? JSON.stringify(body ?? {}) : undefined });
	} catch {
		throw new ApiError(NETWORK_MESSAGE, 0);
	}
	const text = await res.text();
	if (!res.ok) throw new ApiError(errorMessage(res.status, text), res.status);
	return (text ? JSON.parse(text) : undefined) as T;
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

const realApi: Api = {
	bootstrap: () => request<BootstrapView>('GET', '/api/bootstrap'),
	initialOwnerPasskeyLink: () => request<PasskeyLink>('POST', '/api/setup/passkey-link', {}),
	me: () => request<MeView>('GET', '/api/me'),
	passkeyLink: () => request<PasskeyLink>('POST', '/api/me/passkey-link'),
	setupOwner: (name, email) => request<SetupResult>('POST', '/api/setup', { name, email }),
	recover: async (code) => { await request<void>('POST', '/api/recover', { code }); },
	passwordSignIn: async (password) => { await request<void>('POST', '/api/password-sign-in', { password }); },
	signOut: async () => { await request<void>('POST', '/api/logout'); },
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
	settings: async () => list(await request<SettingView[]>('GET', '/api/settings/secrets')),
	setSetting: (name, value) => request('PUT', `/api/settings/secrets/${enc(name)}`, { value }),
	deleteSetting: (name) => request('DELETE', `/api/settings/secrets/${enc(name)}`)
};

export const api: Api = useMock ? mockApi : realApi;
