/// <reference no-default-lib="true"/>
/// <reference lib="esnext" />
/// <reference lib="webworker" />
// Shows Homelab's push notifications (O4) and opens the right screen when one is tapped.
// It caches nothing: the companion always talks to Homelab live.

const sw = self as unknown as ServiceWorkerGlobalScope;

interface Payload { title?: string; body?: string; url?: string; tag?: string; }

sw.addEventListener('install', () => { void sw.skipWaiting(); });
sw.addEventListener('activate', (event) => { event.waitUntil(sw.clients.claim()); });

sw.addEventListener('push', (event) => {
	let data: Payload = {};
	try { data = event.data?.json() ?? {}; } catch { data = { body: event.data?.text() }; }
	event.waitUntil(sw.registration.showNotification(data.title || 'Homelab', {
		body: data.body ?? '',
		tag: data.tag || undefined,
		icon: '/icon-512.png',
		badge: '/icon-512.png',
		data: { url: safePath(data.url) }
	}));
});

sw.addEventListener('notificationclick', (event) => {
	event.notification.close();
	const url = new URL(safePath(event.notification.data?.url), sw.location.origin).href;
	event.waitUntil((async () => {
		const open = await sw.clients.matchAll({ type: 'window', includeUncontrolled: true });
		for (const c of open) {
			if (new URL(c.url).origin === sw.location.origin) { await c.focus(); await (c as WindowClient).navigate(url); return; }
		}
		await sw.clients.openWindow(url);
	})());
});

/** Only paths inside the companion, never another site. */
function safePath(u: unknown): string {
	return typeof u === 'string' && u.startsWith('/') && !u.startsWith('//') ? u : '/';
}
