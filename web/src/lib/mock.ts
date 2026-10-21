// Fake data for developing the UI without the Go server (see api.ts for how it's switched on).
import { ApiError } from './errors';
import type { ActivityEvent, Api, AppView, EnvName, EnvView, PersonInput, KeyView, PersonView, ReleaseView, SecretView, SettingView } from './types';

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const delay = <T>(v: T, ms = 120) => new Promise<T>((r) => setTimeout(() => r(v), ms));

function rel(sha: string, message: string, author: string, minutesAgo: number): ReleaseView {
	return {
		commit: sha.padEnd(40, '0'),
		short: sha.slice(0, 7),
		commitUrl: `https://github.com/rupaai/movie-night/commit/${sha}`,
		image: 'ghcr.io/rupaai/movie-night',
		tag: `sha-${sha}`,
		digest: `sha256:${sha.padEnd(64, 'a')}`,
		message,
		author,
		when: ago(minutesAgo),
		buildUrl: 'https://github.com/rupaai/movie-night/actions/runs/1'
	};
}

const newer = rel('9f3c2ab', 'Show who is watching on the poster', 'Rupa', 42);
const older = rel('4be81d7', 'Add the family watch list', 'Rupa', 60 * 26);
const oldest = rel('1a77c0e', 'First version', 'Rupa', 60 * 24 * 6);

function env(app: string, name: EnvName, o: Partial<EnvView>): EnvView {
	return {
		env: name,
		address: `https://${app}${name === 'dev' ? '-dev' : ''}.example.test`,
		addressName: `${app}-${name}`,
		addressReady: true,
		version: null,
		state: 'running',
		health: 'healthy',
		healthDetail: 'Answered a check a few seconds ago',
		healthSince: ago(300),
		checkedAt: ago(0.2),
		busy: '',
		lastError: '',
		deployedBy: '',
		deployedAt: null,
		deployKind: '',
		canRollback: false,
		missingSecrets: [],
		...o
	};
}

const apps: AppView[] = [
	{
		id: 'movie-night',
		name: 'Movie Night',
		icon: '🎬',
		repo: 'rupaai/movie-night',
		repoUrl: 'https://github.com/rupaai/movie-night',
		promotion: 'auto',
		promoteAfter: '30m',
		autoPromote: { state: 'waiting', at: new Date(Date.now() + 12 * 60_000).toISOString(), detail: 'Promotes 9f3c2ab in 12 min if dev stays healthy' },
		held: [{ commit: 'bad1dea'.padEnd(40, '0'), short: 'bad1dea', reason: 'Its smoke test failed', at: ago(60 * 7) }],
		mirror: { path: '/home/rupaai/.homelab/mirrors/movie-night.git', lastSync: ago(4) },
		latestBuild: { commit: newer.commit, short: newer.short, status: 'passed', url: 'https://github.com/rupaai/movie-night/actions/runs/1', deployed: true },
		envs: [
			env('movie-night', 'dev', { version: newer, deployedBy: 'homelab', deployedAt: ago(38), deployKind: 'deploy', canRollback: true }),
			env('movie-night', 'prod', { missingSecrets: ['TMDB_API_KEY'], version: older, deployedBy: 'rupaai', deployedAt: ago(60 * 26), deployKind: 'promote', canRollback: true })
		]
	},
	{
		id: 'recipe-box',
		name: 'Recipe Box',
		icon: '🍲',
		repo: 'rupaai/recipe-box',
		repoUrl: 'https://github.com/rupaai/recipe-box',
		promotion: 'manual',
		promoteAfter: '30m',
		autoPromote: null,
		held: [],
		mirror: { path: '/home/rupaai/.homelab/mirrors/recipe-box.git', lastSync: ago(60 * 3), error: "The last copy didn't work: GitHub said no" },
		latestBuild: { commit: 'c0ffee1'.padEnd(40, '0'), short: 'c0ffee1', status: 'passed', url: 'https://github.com/rupaai/recipe-box/actions/runs/2', deployed: false },
		envs: [
			env('recipe-box', 'dev', { version: rel('7d2e9aa', 'Scale recipes up and down', 'Rupa', 60 * 5), deployedBy: 'rupaai', deployedAt: ago(60 * 5), deployKind: 'deploy' }),
			env('recipe-box', 'prod', { version: rel('7d2e9aa', 'Scale recipes up and down', 'Rupa', 60 * 5), deployedBy: 'rupaai', deployedAt: ago(60 * 4), deployKind: 'promote' })
		]
	},
	{
		id: 'garden-log',
		name: 'Garden Log',
		icon: '',
		repo: 'rupaai/garden-log',
		repoUrl: 'https://github.com/rupaai/garden-log',
		promotion: 'auto-dev',
		promoteAfter: '1h',
		autoPromote: null,
		held: [],
		mirror: null,
		latestBuild: { commit: 'badc0de'.padEnd(40, '0'), short: 'badc0de', status: 'failed', url: 'https://github.com/rupaai/garden-log/actions/runs/3', deployed: false },
		envs: [
			env('garden-log', 'dev', { version: rel('55aa11f', 'Track watering', 'Rupa', 60 * 30), health: 'degraded', healthDetail: 'Slow to answer: 4 seconds', lastError: '' }),
			env('garden-log', 'prod', {
				version: rel('3e3e3e3', 'Add a plant list', 'Rupa', 60 * 24 * 3),
				health: 'down',
				healthDetail: "Didn't answer the last 3 checks",
				state: 'exited',
				lastError: 'It stopped right after starting. The last lines of its log may say why.'
			})
		]
	}
];

let eventId = 40;
const events: ActivityEvent[] = [
	{ id: 39, at: ago(38), actor: 'homelab', app: 'movie-night', env: 'dev', action: 'deploy.done', detail: '9f3c2ab is running and healthy' },
	{ id: 38, at: ago(40), actor: 'homelab', app: 'movie-night', env: 'dev', action: 'deploy.started', detail: 'New build 9f3c2ab (Show who is watching on the poster) to dev' },
	{ id: 37, at: ago(95), actor: 'homelab', app: 'garden-log', action: 'build.failed', detail: 'The build of badc0de failed, so it won\'t be deployed: https://github.com/rupaai/garden-log/actions/runs/3' },
	{ id: 36, at: ago(60 * 4), actor: 'rupaai', app: 'recipe-box', env: 'prod', action: 'promote.done', detail: '7d2e9aa is running and healthy' },
	{ id: 35, at: ago(60 * 4 + 1), actor: 'rupaai', app: 'recipe-box', env: 'prod', action: 'data.copied', detail: "Copied prod's data to /var/lib/homelab/backups/recipe-box-7d2e9aa" },
	{ id: 34, at: ago(60 * 4 + 2), actor: 'rupaai', app: 'recipe-box', env: 'prod', action: 'promote.started', detail: 'Promote 7d2e9aa (Scale recipes up and down) from dev to prod' },
	{ id: 33, at: ago(60 * 26), actor: 'rupaai', app: 'movie-night', env: 'prod', action: 'promote.done', detail: '4be81d7 is running and healthy' },
	{ id: 32, at: ago(60 * 26 + 3), actor: 'rupaai', app: 'movie-night', env: 'prod', action: 'promote.started', detail: 'Promote 4be81d7 (Add the family watch list) from dev to prod' },
	{ id: 31, at: ago(60 * 30), actor: 'homelab', app: 'garden-log', env: 'prod', action: 'promote.unhealthy', detail: "3e3e3e3 started but isn't healthy: it didn't answer in time" },
	{ id: 30, at: ago(60 * 24 * 6), actor: 'rupaai', app: 'movie-night', action: 'app.added', detail: 'Added Movie Night from rupaai/movie-night' }
];
for (let i = 29; i > 0; i--) {
	events.push({ id: i, at: ago(60 * 24 * 7 + (30 - i) * 90), actor: 'homelab', app: 'recipe-box', env: 'dev', action: 'deploy.done', detail: `Earlier build ${i.toString(16).padStart(7, 'e')} is running and healthy` });
}

type SecretMap = Record<string, Record<EnvName, SecretView[]>>;
const secretStore: SecretMap = {
	'movie-night': {
		dev: [
			{ name: 'TMDB_API_KEY', declared: true, set: true, updatedAt: ago(60 * 24 * 3), updatedBy: 'rupaai' },
			{ name: 'SESSION_SECRET', declared: true, set: true, updatedAt: ago(60 * 24 * 9), updatedBy: 'rupaai' },
			{ name: 'ADMIN_TOKEN', declared: true, set: true, generated: true, value: 'k7m3q-x9rt2-h4wpz-c8nfa', updatedAt: ago(60 * 24), updatedBy: 'homelab' }
		],
		prod: [
			{ name: 'ADMIN_TOKEN', declared: true, set: false, generated: true },
			{ name: 'TMDB_API_KEY', declared: true, set: false },
			{ name: 'SESSION_SECRET', declared: true, set: true, updatedAt: ago(60 * 24 * 9), updatedBy: 'rupaai' },
			{ name: 'OLD_ANALYTICS_KEY', declared: false, set: true, updatedAt: ago(60 * 24 * 40), updatedBy: 'rupaai' }
		]
	}
};
const settingStore: SettingView[] = [
	{ name: 'GITHUB_TOKEN', description: 'Lets Homelab read your repos and builds on GitHub. Make one at github.com/settings/tokens.', set: true, updatedAt: ago(60 * 24 * 12), updatedBy: 'rupaai', appliesAfterRestart: false },
];
const keyStore: KeyView[] = [
	{ id: 'k1', prefix: 'hlk_Photo', name: 'Photo service', kind: 'admin', scopes: ['users:read'], createdBy: 'owner', createdByName: 'Rupa', createdAt: ago(60 * 24 * 20), lastUsed: ago(12) },
	{ id: 'k2', prefix: 'hlk_Stat', name: 'Stats dashboard', kind: 'admin', scopes: ['users:read'], createdBy: 'owner', createdByName: 'Rupa', createdAt: ago(60 * 24 * 4), expires: new Date(Date.now() + 30 * 864e5).toISOString() }
];
const appKeys: KeyView[] = [
	{ id: 'a1', prefix: 'hlk_MvPr', name: 'movies prod', kind: 'app', app: 'movie-night', env: 'prod', scopes: ['users:read'], createdAt: ago(60 * 24 * 30), lastUsed: ago(3) },
	{ id: 'a2', prefix: 'hlk_MvDv', name: 'movies dev', kind: 'app', app: 'movie-night', env: 'dev', scopes: ['users:read'], createdAt: ago(60 * 24 * 30) }
];
const household: PersonView[] = [
	{ id: 'owner', name: 'Rupa', email: 'rupa@example.test', status: 'signed_in', roles: ['owner', 'admin'], grants: {} },
	{ id: 'sam', name: 'Sam', email: 'sam@example.test', status: 'signed_in', roles: ['member'], grants: { 'movie-night': 'use' }, lastSignIn: ago(15) },
	{ id: 'alex', name: 'Alex', email: 'alex@example.test', status: 'invited', roles: ['member'], grants: { 'movie-night': 'use' } }
];

const find = (id: string) => {
	const a = apps.find((x) => x.id === id);
	if (!a) throw new ApiError(`There's no app "${id}".`, 404);
	return a;
};
const envOf = (a: AppView, e: EnvName) => a.envs.find((x) => x.env === e)!;
const log = (actor: string, app: string, e: EnvName, action: string, detail: string) =>
	events.unshift({ id: ++eventId, at: new Date().toISOString(), actor, app, env: e, action, detail });

/** Pretends a change takes a few seconds, like the real thing. */
function work(a: AppView, e: EnvName, busy: string, kind: 'deploy' | 'promote' | 'rollback', to: ReleaseView, verb: string) {
	const ev = envOf(a, e);
	ev.busy = busy;
	log('rupaai', a.id, e, `${kind}.started`, `${verb} ${to.short} (${to.message}) to ${e}`);
	setTimeout(() => {
		const before = ev.version;
		ev.version = to;
		ev.busy = '';
		ev.health = 'healthy';
		ev.healthDetail = 'Answered a check a few seconds ago';
		ev.lastError = '';
		ev.deployedBy = 'rupaai';
		ev.deployedAt = new Date().toISOString();
		ev.deployKind = kind;
		ev.canRollback = before?.commit !== to.commit;
		if (a.latestBuild) a.latestBuild.deployed = envOf(a, 'dev').version?.commit === a.latestBuild.commit;
		log('rupaai', a.id, e, `${kind}.done`, `${to.short} is running and healthy`);
	}, 5000);
}

export const mockApi: Api = {
	bootstrap: () => delay({ needsSetup: false, signInReady: true }),
	initialOwnerPasskeyLink: () => delay({ url: 'http://localhost:7081/lc/mock-token' }),
	me: () => delay({ personId: 'owner', login: 'rupa@example.test', name: 'Rupa', local: true, roles: ['owner', 'admin'] }),
	passkeyLink: () => delay({ url: 'http://localhost:7081/mock-passkey-link' }),
	setupOwner: async () => delay({ loginUrl: 'http://localhost:7081', recoveryCode: '' }),
	recover: async () => delay(undefined),
	passwordSignIn: async () => delay(undefined),
	signOut: async () => delay(undefined),
	people: () => delay(structuredClone(household)),
	async invitePerson(person: PersonInput) {
		const id = `person-${household.length + 1}`;
		const entry: PersonView = { id, ...JSON.parse(JSON.stringify(person)) as PersonInput, status: 'invited' };
		household.push(entry);
		return delay({ id, url: 'http://localhost:7080/mock-invite' });
	},
	redeemInvite: async () => delay({ url: 'http://localhost:7081/mock-passkey-link' }),
	async resendInvite(id: string) {
		if (!household.some((p) => p.id === id && p.status === 'invited')) throw new ApiError('That invite is no longer available.', 400);
		return delay({ url: 'http://localhost:7080/mock-invite' });
	},
	async updatePerson(id: string, person: PersonInput) {
		const entry = household.find((p) => p.id === id);
		if (!entry) throw new ApiError("That person isn't in Homelab.", 404);
		Object.assign(entry, JSON.parse(JSON.stringify(person)) as PersonInput);
		return delay(structuredClone(entry));
	},
	pushKey: () => delay('BMockPublicKeyForTheCompanionInMockMode'),
	subscribePush: () => delay(undefined),
	unsubscribePush: () => delay(undefined),
	testPush: () => delay(1),
	resources: () => delay({
		device: { cpuPercent: 12.4, memUsed: 5.1e9, memTotal: 16e9, diskUsed: 312e9, diskTotal: 494e9 },
		apps: [
			{ app: 'movie-night', env: 'prod', name: 'Movie Night', cpuPercent: 1.8, memUsed: 182e6 },
			{ app: 'movie-night', env: 'dev', name: 'Movie Night', cpuPercent: 0.4, memUsed: 121e6 }
		]
	}),
	device: () => delay({
		system: {
			hostname: 'mars', os: 'Ubuntu 24.04.4 LTS', kernel: '6.8.0-85-generic', uptimeSeconds: 9 * 86400 + 3600, temperatureC: 47,
			disks: [
				{ mount: '/', device: '/dev/mapper/ubuntu--vg-ubuntu--lv', used: 83e9, total: 231e9, health: 'ok' as const },
				{ mount: '/mnt/storage', device: '/dev/sdb1', used: 410e9, total: 984e9, health: 'ok' as const }
			],
			rebootRequired: true, updatesPending: 4, securityPending: 2, reportedAt: new Date().toISOString()
		},
		update: { managed: true, current: 'build-12', latest: 'build-14', available: true, checkedAt: new Date().toISOString(), lastResult: 'Updated to build-12.' }
	}),
	updateHomelab: () => delay('build-14'),
	testVersions: () => delay([{ version: 'pr-17-1a2b3c4', pr: 17, title: 'Test version button' }, { version: 'pr-16-9f8e7d6', pr: 16, title: 'Platform services' }]),
	tryTestVersion: (version: string) => delay({ version, pr: 17, title: 'Test version button' }),
	endTest: (keep: boolean) => delay(keep ? '' : 'build-12'),
	async removePerson(id: string) {
		const entry = household.find((p) => p.id === id);
		if (!entry) throw new ApiError("That person isn't in Homelab.", 404);
		if (entry.roles.includes('owner')) throw new ApiError("The household owner can't be removed.", 400);
		household.splice(household.indexOf(entry), 1);
		return delay([{ name: 'Disable Pocket ID sign-in', done: true }, { name: 'End existing Pocket ID sessions', done: true, detail: 'Pocket ID keeps its own browser session, but the disabled account and removed Homelab sessions prevent further access.' }, { name: 'End Homelab and app sessions', done: true }, { name: 'Remove app access and invitation links', done: true }]);
	},
	apps: () => delay(structuredClone(apps)),
	projects: () => delay(structuredClone(apps)),
	async addApp(repo) {
		if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) throw new ApiError('Use the repo as owner/name, for example rupaai/smart-recommender-player.', 400);
		throw new ApiError(`${repo} has no homelab.yaml on its main branch yet.`, 400);
	},
	async deploy(id) {
		const a = find(id);
		const b = a.latestBuild;
		if (!b || b.status !== 'passed') throw new ApiError("There's no passing build to deploy yet.", 400);
		work(a, 'dev', `Deploying ${b.short}`, 'deploy', rel(b.short, 'Latest build', 'Rupa', 1), 'Deploy');
		await delay(null);
	},
	async promotePreview(id) {
		const a = find(id);
		const dev = envOf(a, 'dev'), prod = envOf(a, 'prod');
		const blockers: string[] = [];
		if (dev.health !== 'healthy') blockers.push("Dev isn't healthy, so this build hasn't proven it works yet.");
		if (prod.version?.digest === dev.version?.digest) blockers.push('Prod already runs this build.');
		if (prod.busy) blockers.push(`Prod is busy: ${prod.busy}.`);
		const same = a.id !== 'movie-night';
		return delay({
			from: structuredClone(prod.version),
			to: structuredClone(dev.version),
			commits: same
				? []
				: [
						{ sha: newer.commit, message: 'Show who is watching on the poster', author: 'Rupa', when: ago(42), url: newer.commitUrl },
						{ sha: 'a1b2c3d'.padEnd(40, '0'), message: 'Fix the search box on small screens', author: 'Sam', when: ago(60 * 3), url: '#' },
						{ sha: 'd4e5f6a'.padEnd(40, '0'), message: 'Remember the last film you picked', author: 'Rupa', when: ago(60 * 9), url: '#' }
					],
			compareUrl: `https://github.com/${a.repo}/compare/${prod.version?.short}...${dev.version?.short}`,
			dataCopy: !!prod.version,
			missingSecrets: [],
			devHealth: dev.health,
			blockers
		});
	},
	async promote(id) {
		const a = find(id);
		const dev = envOf(a, 'dev');
		if (dev.version) work(a, 'prod', `Copying prod's data`, 'promote', dev.version, 'Promote');
		await delay(null);
	},
	async rollbackPreview(id, e) {
		const a = find(id);
		const ev = envOf(a, e);
		const blockers = ev.busy ? [`Busy: ${ev.busy}.`] : [];
		return delay({
			from: structuredClone(ev.version),
			to: e === 'prod' ? structuredClone(oldest) : structuredClone(older),
			blockers,
			note: "The app's data stays as it is now. A copy of prod's data from before each promote is kept on this computer."
		});
	},
	async rollback(id, e) {
		const a = find(id);
		work(a, e, 'Deploying ' + older.short, 'rollback', e === 'prod' ? oldest : older, 'Roll back to');
		await delay(null);
	},
	logs: async (id, e, lines) => {
		const out: string[] = [];
		for (let i = Math.min(lines, 120); i > 0; i--) {
			const t = new Date(Date.now() - i * 7000).toISOString();
			out.push(i % 17 === 0 ? `${t} WARN  slow response from the film database (1.8s)` : `${t} INFO  GET /api/films 200 ${10 + (i % 9)}ms`);
		}
		return delay(`Showing ${id} ${e}\n` + out.join('\n'));
	},
	async setPromotion(id, mode) {
		const a = find(id);
		a.promotion = mode;
		if (mode !== 'auto') a.autoPromote = null;
		else if (!a.autoPromote) a.autoPromote = { state: 'waiting', at: new Date(Date.now() + 30 * 60_000).toISOString(), detail: 'Promotes when dev has stayed healthy' };
		log('rupaai', id, 'prod', 'promotion.changed', `Promotion is now ${mode}`);
		return delay(structuredClone(a));
	},
	async removeApp(id) {
		const a = find(id);
		apps.splice(apps.indexOf(a), 1);
		await delay(null);
	},
	async appSecrets(id) {
		const a = find(id);
		return delay(
			(['dev', 'prod'] as EnvName[]).map((e) => ({
				env: e,
				secrets: (secretStore[a.id]?.[e] ?? []).map((s) => ({ ...s }))
			}))
		);
	},
	async setSecret(id, e, name, value) {
		const a = find(id);
		if (!value) throw new ApiError('Type a value first.', 400);
		const list = (((secretStore[a.id] ??= { dev: [], prod: [] })[e]) ??= []);
		const hit = list.find((s) => s.name === name);
		const now = new Date().toISOString();
		if (hit) Object.assign(hit, { set: true, updatedAt: now, updatedBy: 'rupaai' });
		else list.push({ name, declared: false, set: true, updatedAt: now, updatedBy: 'rupaai' });
		const ev = envOf(a, e);
		ev.missingSecrets = ev.missingSecrets.filter((n) => n !== name);
		await delay(null);
	},
	async deleteSecret(id, e, name) {
		const a = find(id);
		const list = secretStore[a.id]?.[e] ?? [];
		secretStore[a.id][e] = list.filter((s) => s.name !== name);
		await delay(null);
	},
	async regenerateSecret(id, e, name) {
		const hit = secretStore[find(id).id]?.[e]?.find((s) => s.name === name && s.generated);
		if (hit) hit.value = 'k7m3q-x9rt2-h4wpz-c8nfa';
		await delay(null);
	},
	settings: () => delay(settingStore.map((s) => ({ ...s }))),
	async setSetting(name, value) {
		const s = settingStore.find((x) => x.name === name);
		if (!s) throw new ApiError(`There's no setting "${name}".`, 404);
		if (!value) throw new ApiError('Type a value first.', 400);
		Object.assign(s, { set: true, updatedAt: new Date().toISOString(), updatedBy: 'rupaai' });
		await delay(null);
	},
	async deleteSetting(name) {
		const s = settingStore.find((x) => x.name === name);
		if (s) Object.assign(s, { set: false, updatedAt: undefined, updatedBy: undefined });
		await delay(null);
	},
	keys: () => delay({ admin: keyStore.filter((k) => !k.revoked).map((k) => ({ ...k })), app: appKeys.map((k) => ({ ...k })) }),
	async createKey(input) {
		if (!input.name.trim()) throw new ApiError('Give the key a name.', 400);
		const info: KeyView = { id: `k${Date.now()}`, prefix: 'hlk_New1', name: input.name.trim(), kind: 'admin', scopes: [...input.scopes], createdBy: 'owner', createdByName: 'Rupa', createdAt: new Date().toISOString(), expires: input.expires ?? undefined };
		keyStore.push(info);
		return delay({ key: 'hlk_New1mockmockmockmockmockmockmockmockmock', info: { ...info } });
	},
	async revokeKey(id) {
		const k = keyStore.find((x) => x.id === id);
		if (!k) throw new ApiError("Homelab couldn't find that key.", 404);
		k.revoked = true;
		await delay(null);
	},
	async activity(q = {}) {
		let rows = events.filter((e) => !q.app || e.app === q.app);
		if (q.before) rows = rows.filter((e) => e.id < q.before!);
		return delay(rows.slice(0, q.limit ?? 50).map((e) => ({ ...e })));
	}
};
