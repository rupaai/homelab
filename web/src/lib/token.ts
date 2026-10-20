/**
 * The companion's sign-in token. It lives in localStorage (browsers keep that apart for each port),
 * not in a cookie, so other apps on this host never receive it. Every API call sends it as a Bearer header.
 */
const KEY = 'homelab_session';
const FRAGMENT = '#session=';

// Kept in memory too, so the page still works when the browser blocks storage.
let memory = '';

export function getToken(): string {
	try { return localStorage.getItem(KEY) ?? memory; } catch { return memory; }
}

export function setToken(token: string): void {
	memory = token;
	try { localStorage.setItem(KEY, token); } catch { /* storage blocked: the in-memory copy still works */ }
}

export function clearToken(): void {
	memory = '';
	try { localStorage.removeItem(KEY); } catch { /* nothing stored */ }
}

/** After a redirect sign-in the token is in the address as #session=..., which no server ever sees. Keep it and remove it from the address bar. */
export function takeFragmentToken(): void {
	if (typeof location === 'undefined' || !location.hash.startsWith(FRAGMENT)) return;
	const token = decodeURIComponent(location.hash.slice(FRAGMENT.length));
	history.replaceState(history.state, '', location.pathname + location.search);
	if (token) setToken(token);
}
