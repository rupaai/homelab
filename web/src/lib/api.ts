import { ApiError, NETWORK_MESSAGE, errorMessage } from './errors';
import { mockApi } from './mock';
import type { Api, PromotePreview, RollbackPreview } from './types';

/** Mock mode: `?mock` in the URL or VITE_MOCK=1 gives fake data, no server needed. */
export const useMock: boolean =
	import.meta.env.VITE_MOCK === '1' || (typeof location !== 'undefined' && new URLSearchParams(location.search).has('mock'));

async function request<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<T> {
	const headers: Record<string, string> = { Accept: 'application/json' };
	if (method === 'POST') {
		headers['X-Homehub-Request'] = '1';
		headers['Content-Type'] = 'application/json';
	}
	let res: Response;
	try {
		res = await fetch(path, { method, headers, body: method === 'POST' ? JSON.stringify(body ?? {}) : undefined });
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

const realApi: Api = {
	apps: async () => list(await request('GET', '/api/apps')),
	addApp: (repo, branch) => request('POST', '/api/apps', { repo, branch }),
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
	activity: async (q = {}) => {
		const p = new URLSearchParams();
		if (q.app) p.set('app', q.app);
		if (q.before) p.set('before', String(q.before));
		if (q.limit) p.set('limit', String(q.limit));
		return list(await request('GET', `/api/activity?${p}`));
	}
};

export const api: Api = useMock ? mockApi : realApi;
