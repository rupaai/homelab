import { api } from './api';

/** Why push can't work here, or '' when it can. */
export function pushUnsupported(): string {
	if (typeof window === 'undefined') return 'Notifications need a browser.';
	if (!window.isSecureContext) return 'Notifications need the companion’s https address. Open Homelab through Tailscale.';
	if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) {
		const ios = /iPhone|iPad/.test(navigator.userAgent);
		return ios
			? 'On iPhone, add Homelab to your Home Screen first (Share, then Add to Home Screen), then open it from there.'
			: 'This browser can’t show notifications from Homelab.';
	}
	return '';
}

async function registration(): Promise<ServiceWorkerRegistration> {
	return (await navigator.serviceWorker.getRegistration()) ?? navigator.serviceWorker.register('/service-worker.js', { type: 'module' });
}

/** Whether this device already gets notifications. */
export async function pushEnabled(): Promise<boolean> {
	if (pushUnsupported()) return false;
	const reg = await navigator.serviceWorker.getRegistration();
	return !!(await reg?.pushManager.getSubscription());
}

/** Asks permission and subscribes this device. */
export async function enablePush(): Promise<void> {
	const permission = await Notification.requestPermission();
	if (permission !== 'granted') throw new Error('Notifications are blocked for Homelab. Allow them in your browser or phone settings, then try again.');
	const key = await api.pushKey();
	if (!key) throw new Error('Notifications aren’t set up on this Homelab.');
	const reg = await registration();
	await navigator.serviceWorker.ready;
	const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromBase64Url(key) }));
	const json = sub.toJSON();
	await api.subscribePush({ endpoint: sub.endpoint, p256dh: json.keys?.p256dh ?? '', auth: json.keys?.auth ?? '', device: deviceName() });
}

/** Unsubscribes this device. */
export async function disablePush(): Promise<void> {
	const reg = await navigator.serviceWorker.getRegistration();
	const sub = await reg?.pushManager.getSubscription();
	if (!sub) return;
	await api.unsubscribePush(sub.endpoint);
	await sub.unsubscribe();
}

function deviceName(): string {
	const ua = navigator.userAgent;
	if (/iPhone/.test(ua)) return 'iPhone';
	if (/iPad/.test(ua)) return 'iPad';
	if (/Android/.test(ua)) return 'Android';
	if (/Mac/.test(ua)) return 'Mac';
	if (/Windows/.test(ua)) return 'Windows';
	return 'Browser';
}

function fromBase64Url(s: string): Uint8Array<ArrayBuffer> {
	const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4));
	const out = new Uint8Array(new ArrayBuffer(b.length));
	for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i);
	return out;
}
