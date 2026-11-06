import { describe, expect, it } from 'vitest';
import { groupPorts, isOpen, openSummary, portText, reachWords, serviceWords } from './network';
import type { PortRow } from './types';

const row = (o: Partial<PortRow>): PortRow => ({ app: 'nl', appName: 'Northern Lights', env: 'prod', service: 'app', hostPort: '8000', containerPort: '8000', protocol: 'tcp', reach: 'this device only', ...o });

describe('portText', () => {
	it('shows a port or a range with its protocol', () => {
		expect(portText(row({}))).toBe('8000 tcp');
		expect(portText(row({ hostPort: '6881-6891', protocol: 'udp' }))).toBe('6881-6891 udp');
	});
});

describe('reach', () => {
	it('says who can reach a port', () => {
		expect(isOpen(row({ reach: 'home network' }))).toBe(true);
		expect(isOpen(row({}))).toBe(false);
		expect(reachWords(row({ reach: 'home network' }))).toBe('Home network');
		expect(reachWords(row({}))).toBe('This device only');
	});
});

describe('serviceWords', () => {
	it('uses plain words and keeps service names', () => {
		expect(serviceWords(row({}))).toBe('The app');
		expect(serviceWords(row({ service: 'db' }))).toBe('Database');
		expect(serviceWords(row({ service: 'address' }))).toBe('Web address');
		expect(serviceWords(row({ service: 'prowlarr' }))).toBe('prowlarr');
		expect(serviceWords(row({ app: 'homelab', service: 'app door' }))).toBe('App door');
	});
});

describe('groupPorts', () => {
	it('puts Homelab first and groups by app and environment', () => {
		const groups = groupPorts([
			row({ service: 'app' }),
			row({ env: 'dev' }),
			row({ service: 'jellyfin', hostPort: '8096' }),
			row({ app: 'homelab', appName: 'Homelab', env: '', service: 'companion', hostPort: '7080' })
		]);
		expect(groups.map((g) => g.title)).toEqual(['Homelab', 'Northern Lights · prod', 'Northern Lights · dev']);
		expect(groups[1].rows).toHaveLength(2);
	});
	it('is empty without rows', () => expect(groupPorts([])).toEqual([]));
});

describe('openSummary', () => {
	it('counts the open ports', () => {
		expect(openSummary([])).toBe('Nothing is open to the home network.');
		expect(openSummary([row({ reach: 'home network' })])).toBe('1 port is open to the home network.');
		expect(openSummary([row({ reach: 'home network' }), row({ reach: 'home network' }), row({})])).toBe('2 ports are open to the home network.');
	});
});
