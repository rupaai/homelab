import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { clearToken, getToken, setToken, takeFragmentToken } from './token';

const store = new Map<string, string>();

beforeEach(() => {
	store.clear();
	vi.stubGlobal('localStorage', {
		getItem: (k: string) => store.get(k) ?? null,
		setItem: (k: string, v: string) => void store.set(k, v),
		removeItem: (k: string) => void store.delete(k)
	});
	clearToken();
});
afterEach(() => vi.unstubAllGlobals());

const json = (status: number, body?: unknown) =>
	vi.fn().mockResolvedValue(new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }));

describe('session token', () => {
	it('is taken from the address fragment and removed from the address bar', () => {
		const replaceState = vi.fn();
		vi.stubGlobal('location', { hash: '#session=hls_abc', pathname: '/home', search: '?x=1' });
		vi.stubGlobal('history', { state: null, replaceState });
		takeFragmentToken();
		expect(getToken()).toBe('hls_abc');
		expect(store.get('homelab_session')).toBe('hls_abc');
		expect(replaceState).toHaveBeenCalledWith(null, '', '/home?x=1');
	});

	it('leaves other fragments alone', () => {
		const replaceState = vi.fn();
		vi.stubGlobal('location', { hash: '#top', pathname: '/', search: '' });
		vi.stubGlobal('history', { state: null, replaceState });
		takeFragmentToken();
		expect(getToken()).toBe('');
		expect(replaceState).not.toHaveBeenCalled();
	});

	it('is sent on every API call', async () => {
		setToken('hls_abc');
		const fetchMock = json(200, []);
		vi.stubGlobal('fetch', fetchMock);
		await api.apps();
		expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe('Bearer hls_abc');
	});

	it('is not sent when signed out', async () => {
		const fetchMock = json(200, []);
		vi.stubGlobal('fetch', fetchMock);
		await api.apps();
		expect(fetchMock.mock.calls[0][1].headers.Authorization).toBeUndefined();
	});

	it('is stored after a password sign-in', async () => {
		vi.stubGlobal('fetch', json(200, { token: 'hls_new' }));
		await api.passwordSignIn('pw');
		expect(getToken()).toBe('hls_new');
	});

	it('is stored after a recovery-code sign-in', async () => {
		vi.stubGlobal('fetch', json(200, { token: 'hls_rec' }));
		await api.recover('code');
		expect(getToken()).toBe('hls_rec');
	});

	it('is cleared on sign-out', async () => {
		setToken('hls_abc');
		vi.stubGlobal('fetch', json(204));
		await api.signOut();
		expect(getToken()).toBe('');
		expect(store.has('homelab_session')).toBe(false);
	});

	it('is cleared when the server says to sign in again', async () => {
		setToken('hls_old');
		vi.stubGlobal('fetch', json(403, { error: 'Sign in to use Homelab.' }));
		await expect(api.apps()).rejects.toThrow();
		expect(getToken()).toBe('');
	});
});
