/** The main tabs of the companion. Pure, so it can be tested without a browser. */

export interface NavTab {
	href: string;
	label: string;
	icon: string;
	match: (path: string) => boolean;
}

const under = (path: string, base: string) => path === base || path.startsWith(`${base}/`);

const apps: NavTab = {
	href: '/',
	label: 'Apps',
	icon: 'M3 5h7v6H3zM14 5h7v6h-7zM3 15h7v6H3zM14 15h7v6h-7z',
	match: (p) => p === '/' || under(p, '/home') || under(p, '/apps')
};
const activity: NavTab = {
	href: '/activity',
	label: 'Activity',
	icon: 'M4 12h4l3-8 4 16 3-8h2',
	match: (p) => under(p, '/activity') || under(p, '/logs')
};
const storage: NavTab = {
	href: '/files',
	label: 'Storage',
	icon: 'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6',
	match: (p) => under(p, '/files') || under(p, '/databases')
};
const settings: NavTab = {
	href: '/settings',
	label: 'Settings',
	icon: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l-.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
	match: (p) => under(p, '/settings') || under(p, '/resources') || under(p, '/people') || under(p, '/me')
};

/** Admins are owners and admins. */
export const isAdmin = (roles: string[]): boolean => roles.some((r) => r === 'owner' || r === 'admin');

/** The tabs a person sees. With one tab or none, the layout shows no tab bar. */
export function navTabs(roles: string[]): NavTab[] {
	return isAdmin(roles) ? [apps, activity, storage, settings] : [apps];
}
