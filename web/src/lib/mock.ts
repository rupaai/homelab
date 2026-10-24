// Fake data for developing the UI without the Go server (see api.ts for how it's switched on).
import { ApiError } from './errors';
import type { ActivityEvent, Api, DbChange, DbCopy, DbInfo, DbTable, AppView, SavedSecretView, FileFolder, StorageDisk, StoredFile, UploadResult, EnvName, EnvView, PersonInput, KeyView, PersonView, ReleaseView, SecretView, SettingView, SecretVersion, LogLevel, LogLine, LogCounts } from './types';

const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const delay = <T>(v: T, ms = 120) => new Promise<T>((r) => setTimeout(() => r(v), ms));

function rel(sha: string, message: string, author: string, minutesAgo: number, version?: string, summary?: string, notes?: string): ReleaseView {
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
		buildUrl: 'https://github.com/rupaai/movie-night/actions/runs/1',
		...(version ? { version, summary, notes } : {})
	};
}

const newNotes = '## New\n\n- Show who is watching on the poster (#31)\n- Remember the last film you picked (#30)\n\n## Fixes\n\n- Fix the search box on small screens (#29)\n';
const newer = rel('9f3c2ab', 'Show who is watching on the poster', 'Rupa', 42, 'v0.4.0-dev.2', '2 new, 1 fix', newNotes);
const older = rel('4be81d7', 'Add the family watch list', 'Rupa', 60 * 26, 'v0.3.0', '1 new', '## New\n\n- Add the family watch list (#27)\n');
const oldest = rel('1a77c0e', 'First version', 'Rupa', 60 * 24 * 6, 'v0.2.1', '1 fix', '## Fixes\n\n- First version (#20)\n');
/** The prod version of a dev one: v0.4.0-dev.2 becomes v0.4.0. */
const prodVersion = (v?: string) => (v ?? '').replace(/-dev\.\d+$/, '');

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
		versionProblem: '',
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
		versionProblem: '',
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
		versionProblem: "Can't make versions on GitHub: the token needs Contents: read and write.",
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
	{ id: 39, at: ago(38), actor: 'homelab', app: 'movie-night', env: 'dev', action: 'deploy.done', detail: 'v0.4.0-dev.2 is running and healthy' },
	{ id: 38, at: ago(40), actor: 'homelab', app: 'movie-night', env: 'dev', action: 'deploy.started', detail: 'New build v0.4.0-dev.2 (Show who is watching on the poster) to dev' },
	{ id: 37, at: ago(95), actor: 'homelab', app: 'garden-log', action: 'build.failed', detail: 'The build of badc0de failed, so it won\'t be deployed: https://github.com/rupaai/garden-log/actions/runs/3' },
	{ id: 36, at: ago(60 * 4), actor: 'rupaai', app: 'recipe-box', env: 'prod', action: 'promote.done', detail: '7d2e9aa is running and healthy' },
	{ id: 35, at: ago(60 * 4 + 1), actor: 'rupaai', app: 'recipe-box', env: 'prod', action: 'data.copied', detail: "Copied prod's data to /var/lib/homelab/backups/recipe-box-7d2e9aa" },
	{ id: 34, at: ago(60 * 4 + 2), actor: 'rupaai', app: 'recipe-box', env: 'prod', action: 'promote.started', detail: 'Promote 7d2e9aa (Scale recipes up and down) from dev to prod' },
	{ id: 33, at: ago(60 * 26), actor: 'rupaai', app: 'movie-night', env: 'prod', action: 'promote.done', detail: 'v0.3.0 is running and healthy' },
	{ id: 32, at: ago(60 * 26 + 3), actor: 'rupaai', app: 'movie-night', env: 'prod', action: 'promote.started', detail: 'Promote v0.3.0 (Add the family watch list) from dev to prod' },
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
			{ name: 'ADMIN_TOKEN', declared: true, set: true, generated: true, updatedAt: ago(60 * 24), updatedBy: 'homelab' }
		],
		prod: [
			{ name: 'ADMIN_TOKEN', declared: true, set: false, generated: true },
			{ name: 'TMDB_API_KEY', declared: true, set: false },
			{ name: 'SESSION_SECRET', declared: true, set: true, updatedAt: ago(60 * 24 * 9), updatedBy: 'rupaai' },
			{ name: 'OLD_ANALYTICS_KEY', declared: false, set: true, updatedAt: ago(60 * 24 * 40), updatedBy: 'rupaai' }
		]
	}
};
// Secrets the app saved itself, by "app/env". Values are kept apart from the list.
const savedStore: Record<string, (SavedSecretView & { value: string })[]> = {
	'movie-night/prod': [
		{ name: 'google_token:user42', version: 3, updatedAt: ago(60 * 5), value: 'ya29.mock-token-user42' },
		{ name: 'google_token:user77', version: 1, updatedAt: ago(60 * 24 * 2), value: 'ya29.mock-token-user77' }
	]
};
// The mock keeps values and versions apart from the list, like the real server does.
const mockValues: Record<string, (SecretVersion & { value: string })[]> = {
	'movie-night/dev/TMDB_API_KEY': [{ version: 1, setBy: 'rupaai', setAt: ago(60 * 24 * 8), value: 'tmdb-old-1234' }, { version: 2, setBy: 'rupaai', setAt: ago(60 * 24 * 3), value: 'tmdb-dev-5678' }],
	'movie-night/dev/ADMIN_TOKEN': [{ version: 1, setBy: 'homelab', setAt: ago(60 * 24), value: 'k7m3q-x9rt2-h4wpz-c8nfa' }],
	'movie-night/prod/SESSION_SECRET': [{ version: 1, setBy: 'rupaai', setAt: ago(60 * 24 * 9), value: 'session-prod-abcdef' }],
	'settings/GITHUB_TOKEN': [{ version: 1, setBy: 'rupaai', setAt: ago(60 * 24 * 12), value: 'ghp_mockmockmock' }]
};
function addVersion(key: string, value: string, by: string): number {
	const list = (mockValues[key] ??= []);
	const n = (list.at(-1)?.version ?? 0) + 1;
	list.push({ version: n, setBy: by, setAt: new Date().toISOString(), value });
	if (list.length > 5) list.splice(0, list.length - 5);
	return n;
}
function mockVersions(key: string): SecretVersion[] {
	return [...(mockValues[key] ?? [])].reverse().map(({ version, setBy, setAt }) => ({ version, setBy, setAt }));
}
function mockRestore(key: string, version: number, by: string): void {
	const hit = mockValues[key]?.find((v) => v.version === version);
	if (!hit) throw new ApiError(`There's no version ${version} of that secret.`, 400);
	addVersion(key, hit.value, by);
}
const settingStore: SettingView[] = [
	{ name: 'GITHUB_TOKEN', description: 'Lets Homelab read your repos and builds on GitHub. Make one at github.com/settings/tokens.', set: true, updatedAt: ago(60 * 24 * 12), updatedBy: 'rupaai', appliesAfterRestart: false },
	{ name: 'EMAIL_ADDRESS', description: 'The Gmail address Homelab sends email from.', set: false, appliesAfterRestart: false },
	{ name: 'EMAIL_APP_PASSWORD', description: 'A Gmail app password. Make one at https://myaccount.google.com/apppasswords.', set: false, appliesAfterRestart: false },
];
const keyStore: KeyView[] = [
	{ id: 'k1', prefix: 'hlk_Photo', name: 'Photo service', kind: 'admin', scopes: ['users:read'], createdBy: 'owner', createdByName: 'Rupa', createdAt: ago(60 * 24 * 20), lastUsed: ago(12) },
	{ id: 'k2', prefix: 'hlk_Stat', name: 'Stats dashboard', kind: 'admin', scopes: ['users:read'], createdBy: 'owner', createdByName: 'Rupa', createdAt: ago(60 * 24 * 4), expires: new Date(Date.now() + 30 * 864e5).toISOString() }
];
const appKeys: KeyView[] = [
	{ id: 'a1', prefix: 'hlk_MvPr', name: 'movies prod', kind: 'app', app: 'movie-night', env: 'prod', scopes: ['users:read'], createdAt: ago(60 * 24 * 30), lastUsed: ago(3) },
	{ id: 'a2', prefix: 'hlk_MvDv', name: 'movies dev', kind: 'app', app: 'movie-night', env: 'dev', scopes: ['users:read'], createdAt: ago(60 * 24 * 30) }
];
const disks: StorageDisk[] = [
	{ name: 'main', path: '/var/lib/homelab', kind: 'data', used: 210e9, total: 500e9 },
	{ name: 'photos', path: '/mnt/photos', kind: 'storage', used: 880e9, total: 1e12 },
	{ name: 'backup', path: '/mnt/backup', kind: 'storage', used: 1.9e12, total: 2e12, problem: 'This disk is almost full.' },
	{ name: 'nas', path: '/mnt/nas', kind: 'storage', used: 3e12, total: 8e12, shared: true }
];
const folderStore: FileFolder[] = [
	{ name: 'documents', disk: 'main', app: '', env: '', files: 3, bytes: 0, created_at: ago(60 * 24 * 30) },
	{ name: 'movie-night-posters', disk: 'photos', app: 'movie-night', env: 'prod', files: 0, bytes: 0, created_at: ago(60 * 24 * 5) }
];
const dbList: DbInfo[] = [
	{ app: 'movie-night', env: 'dev', engine: 'postgres', status: 'ok', size_bytes: 8_400_000, connections: 2, tables: 2 },
	{ app: 'movie-night', env: 'prod', engine: 'postgres', status: 'ok', size_bytes: 52_000_000, connections: 4, tables: 2 }
];
const dbTableList: DbTable[] = [
	{ schema: 'public', name: 'movies', rows: 120 },
	{ schema: 'public', name: 'votes', rows: 3 }
];
const dbCopies: Record<string, DbCopy[]> = {
	dev: [{ name: '20261023-120000-manual', made: ago(90), size_bytes: 2_100_000, kind: 'manual' }],
	prod: [{ name: '20261022-090000-promote', made: ago(60 * 27), size_bytes: 9_800_000, kind: 'promote' }]
};
const nowSec = () => Math.floor(Date.now() / 1000);
const dbChangeStore: DbChange[] = [
	{ id: 4, person: 'owner', at: nowSec() - 300, sql: "delete from votes where movie_id = 9", ok: true, rows: 3, ms: 8, error: '', copy: '20261023-120500-before-write' },
	{ id: 3, person: 'owner', at: nowSec() - 3600, sql: 'update movies set title = oops', ok: false, rows: 0, ms: 2, error: 'column "oops" does not exist', copy: null },
	{ id: 2, person: 'rupaai', at: nowSec() - 86400, sql: "update movies set year = 2021 where title = 'Dune'", ok: true, rows: 1, ms: 5, error: '', copy: null },
	{ id: 1, person: 'rupaai', at: nowSec() - 90000, sql: "insert into movies (title) values ('Heat')", ok: true, rows: 1, ms: 4, error: '', copy: null }
];
const fileStore: StoredFile[] = [
	{ id: 'f005', folder: 'documents', name: 'harbour.jpg', content_type: 'image/jpeg', size: 3_400_000, sha256: 'd'.repeat(64), created_at: ago(10), created_by: 'owner', tags: ['trip'], info: {}, meta: { width: 4032, height: 3024, taken_at: '2026-10-03T14:21:07Z', camera_make: 'Apple', camera_model: 'iPhone 15 Pro', lens: 'iPhone 15 Pro back triple camera 6.86mm f/1.78', orientation: 1, gps: { lat: 55.67594, lon: 12.56553, alt: 12 } } },
	{ id: 'f004', folder: 'documents', name: 'birthday.mp4', content_type: 'video/mp4', size: 24_800_000, sha256: 'e'.repeat(64), created_at: ago(20), created_by: 'owner', tags: [], info: {}, meta: { width: 1920, height: 1080, taken_at: '2026-08-14T18:02:00Z', camera_make: 'Canon', camera_model: 'Canon EOS R6', duration_s: 151, video_codec: 'h264' } },
	{ id: 'f003', folder: 'documents', name: 'holiday.svg', content_type: 'image/svg+xml', size: 1200, sha256: 'a'.repeat(64), created_at: ago(30), created_by: 'owner', tags: ['trip'], info: {}, meta: { width: 120, height: 80 } },
	{ id: 'f002', folder: 'documents', name: 'insurance.pdf', content_type: 'application/pdf', size: 482_000, sha256: 'b'.repeat(64), created_at: ago(60 * 30), created_by: 'owner', tags: ['home', 'paperwork'], info: {}, meta: { pages: 12, title: 'Home insurance policy', author: 'Northgate Insurance' } },
	{ id: 'f001', folder: 'documents', name: 'notes.txt', content_type: 'text/plain', size: 2300, sha256: 'c'.repeat(64), created_at: ago(60 * 24 * 12), created_by: 'owner', tags: [], info: {}, meta: {} }
];
const syncFolders = () => folderStore.forEach((f) => {
	const rows = fileStore.filter((x) => x.folder === f.name);
	f.files = rows.length;
	f.bytes = rows.reduce((n, x) => n + x.size, 0);
});
syncFolders();
const folderOf = (name: string) => {
	const f = folderStore.find((x) => x.name === name);
	if (!f) throw new ApiError("Homelab couldn't find that folder.", 404);
	return f;
};
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
	log('rupaai', a.id, e, `${kind}.started`, `${verb} ${to.version || to.short} (${to.message}) to ${e}`);
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
		log('rupaai', a.id, e, `${kind}.done`, `${to.version || to.short} is running and healthy`);
	}, 5000);
}


// A few sample log lines, oldest first. Ids go up with time.
const logSeed: [number, LogLevel, string, string, string, string, string, Record<string, unknown>][] = [
	[240, 'info', 'movie-night', 'prod', 'app', 'stdout', 'Listening on port 8080', {}],
	[200, 'debug', 'movie-night', 'prod', 'app', 'stdout', 'Loaded 42 films from the cache', { films: 42 }],
	[150, 'warn', 'movie-night', 'prod', 'app', 'stderr', 'Slow response from the film database (1.8s)', { ms: 1800 }],
	[90, 'error', 'movie-night', 'prod', 'app', 'stderr', 'Could not load poster for "Heat": connection reset', { film: 'Heat', retry: true }],
	[60, 'info', 'movie-night', 'dev', 'app', 'stdout', 'GET /api/films 200 12ms', { path: '/api/films', status: 200 }],
	[45, 'fatal', 'recipe-box', 'dev', 'app', 'stderr', 'Out of memory, shutting down', {}],
	[40, 'info', 'homelab', '', 'deploys', 'event', 'v0.4.0-dev.2 is running and healthy', {}],
	[30, 'info', 'homelab', '', 'files', 'event', 'File saved: poster.jpg (212 KB) in movie-night-posters', { size: 212000 }],
	[20, 'unknown', 'recipe-box', 'prod', 'app', 'stdout', 'A line without a level', {}],
	[5, 'info', 'movie-night', 'prod', 'app', 'stdout', 'GET /api/films 200 10ms', { path: '/api/films', status: 200 }]
];
const logRows: LogLine[] = logSeed.map(([min, level, app, env, service, stream, msg, extra], i) => ({ id: i + 1, time: ago(min), level, msg, app, env, service, stream, extra }));
const levelNumber: Record<LogLevel, number> = { unknown: 0, debug: 1, info: 2, warn: 3, error: 4, fatal: 5 };

export const mockApi: Api = {
	bootstrap: () => delay({ needsSetup: false, signInReady: true }),
	initialOwnerPasskeyLink: () => delay({ url: 'http://localhost:7081/lc/mock-token' }),
	me: () => delay({ personId: 'owner', login: 'rupa@example.test', name: 'Rupa', local: true, roles: ['owner', 'admin'], canSeeSecrets: true }),
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
		return delay({ id, url: 'http://localhost:7080/mock-invite', emailed: !!person.email });
	},
	redeemInvite: async () => delay({ url: 'http://localhost:7081/mock-passkey-link' }),
	setInvitePassword: async () => delay(undefined),
	testEmail: async () => delay(undefined),
	async resendInvite(id: string) {
		if (!household.some((p) => p.id === id && p.status === 'invited')) throw new ApiError('That invite is no longer available.', 400);
		return delay({ url: 'http://localhost:7080/mock-invite', emailed: false, emailError: "Email isn't set up, so send the link yourself." });
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
		update: { managed: true, current: 'v0.20.0', latest: 'v0.21.0', available: true, checkedAt: new Date().toISOString(), lastResult: 'Updated to v0.20.0.',
			changes: { breaking: 0, new: 2, fixes: 1, notes: '## New\n\n- Show what an update changes (#31)\n- Pick release versions from PR titles (#30)\n\n## Fixes\n\n- Keep the test version when Homelab restarts (#29)' } }
	}),
	updateHomelab: () => delay('v0.21.0'),
	testVersions: () => delay([{ version: 'v0.21.0-pr.17.1a2b3c4', pr: 17, title: 'Test version button' }, { version: 'v0.21.0-pr.16.9f8e7d6', pr: 16, title: 'Platform services' }]),
	tryTestVersion: (version: string) => delay({ version, pr: 17, title: 'Test version button' }),
	endTest: (keep: boolean) => delay(keep ? '' : 'v0.20.0'),
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
		const dev = envOf(a, 'dev').version?.version;
		const next = dev ? dev.replace(/\.(\d+)$/, (_, n) => `.${Number(n) + 1}`) : undefined;
		work(a, 'dev', `Deploying ${next ?? b.short}`, 'deploy', rel(b.short, 'Latest build', 'Rupa', 1, next, '1 fix', '## Fixes\n\n- Latest build (#32)\n'), 'Deploy');
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
			blockers,
			nextVersion: dev.version?.version ? prodVersion(dev.version.version) : '',
			summary: dev.version?.version ? (dev.version.summary ?? '') : '',
			notes: dev.version?.version ? (dev.version.notes ?? '') : ''
		});
	},
	async promote(id) {
		const a = find(id);
		const dev = envOf(a, 'dev');
		if (dev.version) work(a, 'prod', `Copying prod's data`, 'promote', { ...dev.version, ...(dev.version.version ? { version: prodVersion(dev.version.version) } : {}) }, 'Promote');
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
		work(a, e, 'Deploying ' + (e === 'prod' ? oldest : older).version, 'rollback', e === 'prod' ? oldest : older, 'Roll back to');
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
		return delay({
			envs: (['dev', 'prod'] as EnvName[]).map((e) => ({
				env: e,
				secrets: (secretStore[a.id]?.[e] ?? []).map((s) => ({ ...s })),
				saved: (savedStore[`${a.id}/${e}`] ?? []).map(({ value: _value, ...rest }) => ({ ...rest }))
			})),
			canSeeValues: true
		});
	},
	async revealSecret(id, e, name) {
		const list = mockValues[`${find(id).id}/${e}/${name}`];
		if (!list?.length) throw new ApiError(`${name} isn't set for ${e}.`, 400);
		return delay(list[list.length - 1].value);
	},
	secretVersions: async (id, e, name) => delay(mockVersions(`${find(id).id}/${e}/${name}`)),
	async restoreSecret(id, e, name, version) {
		mockRestore(`${find(id).id}/${e}/${name}`, version, 'rupaai');
		const hit = secretStore[find(id).id]?.[e]?.find((s) => s.name === name);
		if (hit) Object.assign(hit, { updatedAt: new Date().toISOString(), updatedBy: 'rupaai' });
		await delay(null);
	},
	async setSecret(id, e, name, value) {
		const a = find(id);
		if (!value) throw new ApiError('Type a value first.', 400);
		const list = (((secretStore[a.id] ??= { dev: [], prod: [] })[e]) ??= []);
		const hit = list.find((s) => s.name === name);
		const now = new Date().toISOString();
		addVersion(`${a.id}/${e}/${name}`, value, 'rupaai');
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
		delete mockValues[`${a.id}/${e}/${name}`];
		await delay(null);
	},
	async revealSavedSecret(id, e, name) {
		const hit = savedStore[`${find(id).id}/${e}`]?.find((s) => s.name === name);
		if (!hit) throw new ApiError(`The app hasn't saved ${name} for ${e}.`, 400);
		return delay(hit.value);
	},
	async deleteSavedSecret(id, e, name) {
		const key = `${find(id).id}/${e}`;
		savedStore[key] = (savedStore[key] ?? []).filter((s) => s.name !== name);
		await delay(null);
	},
	async regenerateSecret(id, e, name) {
		const a = find(id);
		const hit = secretStore[a.id]?.[e]?.find((s) => s.name === name && s.generated);
		if (hit) addVersion(`${a.id}/${e}/${name}`, 'n3w-t0k3n-x9rt2-h4wpz', 'rupaai');
		await delay(null);
	},
	settings: () => delay(settingStore.map((s) => ({ ...s }))),
	async setSetting(name, value) {
		const s = settingStore.find((x) => x.name === name);
		if (!s) throw new ApiError(`There's no setting "${name}".`, 404);
		if (!value) throw new ApiError('Type a value first.', 400);
		addVersion(`settings/${name}`, value, 'rupaai');
		Object.assign(s, { set: true, updatedAt: new Date().toISOString(), updatedBy: 'rupaai' });
		await delay(null);
	},
	async revealSetting(name) {
		const list = mockValues[`settings/${name}`];
		if (!list?.length) throw new ApiError(`${name} isn't set.`, 400);
		return delay(list[list.length - 1].value);
	},
	settingVersions: async (name) => delay(mockVersions(`settings/${name}`)),
	async restoreSetting(name, version) {
		mockRestore(`settings/${name}`, version, 'rupaai');
		await delay(null);
	},
	async deleteSetting(name) {
		const s = settingStore.find((x) => x.name === name);
		if (s) Object.assign(s, { set: false, updatedAt: undefined, updatedBy: undefined });
		delete mockValues[`settings/${name}`];
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
	},
	async searchLogs(q) {
		const words = (q.q ?? '').toLowerCase().split(/\s+/).filter(Boolean);
		const since = q.since ? (/^\d+$/.test(q.since) ? Number(q.since) : Date.parse(q.since)) : 0;
		let rows = logRows.filter((l) => (!q.app || l.app === q.app) && (!q.env || l.env === q.env) && (!q.service || l.service === q.service) && Date.parse(l.time) >= since);
		rows = rows.filter((l) => words.every((w) => `${l.msg} ${JSON.stringify(l.extra)}`.toLowerCase().includes(w)));
		const counts: LogCounts = { debug: 0, info: 0, warn: 0, error: 0, fatal: 0, unknown: 0 };
		for (const l of rows) counts[l.level]++;
		if (q.level?.length) rows = rows.filter((l) => q.level!.includes(l.level));
		if (q.minLevel) rows = rows.filter((l) => l.level === 'unknown' || levelNumber[l.level] >= levelNumber[q.minLevel!]);
		if (q.before !== undefined) rows = rows.filter((l) => l.id < q.before!);
		if (q.after !== undefined) rows = rows.filter((l) => l.id > q.after!);
		rows = [...rows].sort((a, b) => b.id - a.id);
		const limit = q.limit ?? 100;
		const page = rows.slice(0, limit);
		return delay({ lines: page.map((l) => ({ ...l, extra: { ...l.extra } })), counts, next: rows.length > limit ? String(page[page.length - 1].id) : null });
	},
	storage: () => delay(disks.map((d) => ({ ...d }))),
	fileFolders: () => delay(folderStore.map((f) => ({ ...f }))),
	async createFileFolder(name, disk) {
		if (folderStore.some((f) => f.name === name)) throw new ApiError('There is already a folder with that name.', 409);
		const f: FileFolder = { name, disk: disk || 'main', app: '', env: '', files: 0, bytes: 0, created_at: new Date().toISOString() };
		folderStore.push(f);
		return delay({ ...f });
	},
	async deleteFileFolder(name) {
		const i = folderStore.findIndex((f) => f.name === name);
		if (i < 0) throw new ApiError(`There's no folder ${name}.`, 404);
		if (fileStore.some((f) => f.folder === name)) throw new ApiError('The folder still has files. Delete them first.', 409);
		folderStore.splice(i, 1);
		await delay(null);
	},
	async files(folder, q = {}) {
		folderOf(folder);
		let rows = fileStore.filter((f) => f.folder === folder && (!q.tag || f.tags.includes(q.tag)) && (!q.type || f.content_type.startsWith(q.type)));
		const day = (v: string) => (/^\d{4}-\d{2}-\d{2}$/.test(v) ? 864e5 : 0);
		if (q.takenAfter) rows = rows.filter((f) => !!f.meta.taken_at && Date.parse(f.meta.taken_at) >= Date.parse(q.takenAfter!));
		if (q.takenBefore) rows = rows.filter((f) => !!f.meta.taken_at && Date.parse(f.meta.taken_at) < Date.parse(q.takenBefore!) + day(q.takenBefore!));
		if (q.camera) rows = rows.filter((f) => `${f.meta.camera_make ?? ''} ${f.meta.camera_model ?? ''}`.toLowerCase().includes(q.camera!.trim().toLowerCase()));
		if (q.after) rows = rows.filter((f) => f.id < q.after!);
		const limit = q.limit ?? 50;
		const page = rows.slice(0, limit);
		return delay({ files: page.map((f) => ({ ...f, tags: [...f.tags], meta: { ...f.meta } })), next: rows.length > limit ? page[page.length - 1].id : null });
	},
	async uploadFile(folder, file, tags = []): Promise<UploadResult> {
		folderOf(folder);
		if (file.size > 50e6) throw new ApiError('That file is too big. The limit is 50 MB.', 413);
		const same = fileStore.find((x) => x.folder === folder && x.name === file.name && x.size === file.size);
		if (same) return delay({ file: { ...same, tags: [...same.tags], meta: { ...same.meta } }, duplicate: true });
		const f: StoredFile = { id: `f${String(Date.now()).slice(-9)}`, folder, name: file.name, content_type: file.type || 'application/octet-stream', size: file.size, sha256: '0'.repeat(64), created_at: new Date().toISOString(), created_by: 'owner', tags: [...tags], info: {}, meta: {} };
		fileStore.unshift(f);
		syncFolders();
		return delay({ file: { ...f }, duplicate: false });
	},
	async fileBlob() {
		const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 80"><rect width="120" height="80" fill="#8ab"/><circle cx="85" cy="25" r="10" fill="#fd8"/><path d="M0 80l40-40 30 30 20-15 30 25z" fill="#465"/></svg>';
		return delay(new Blob([svg], { type: 'image/svg+xml' }));
	},
	async deleteFile(folder, id) {
		const i = fileStore.findIndex((f) => f.folder === folder && f.id === id);
		if (i < 0) throw new ApiError("Homelab couldn't find that file.", 404);
		fileStore.splice(i, 1);
		syncFolders();
		await delay(null);
	},
	databases: () => delay(dbList.map((d) => ({ ...d }))),
	async database(app, env) {
		const d = dbList.find((x) => x.app === app && x.env === env);
		if (!d) throw new ApiError("There's no database there.", 404);
		return delay({ ...d });
	},
	dbTables: () => delay(dbTableList.map((t) => ({ ...t }))),
	async dbRows(_app, _env, table, offset, limit = 50) {
		const all = Array.from({ length: table.rows }, (_, i) => [String(i + 1), `${table.name} ${i + 1}`]);
		return delay({ columns: ['id', 'title'], rows: all.slice(offset, offset + limit), truncated: false });
	},
	async dbQuery(_app, env, sql, write, confirm) {
		if (write && /^\s*(delete|update)\b(?!.*\bwhere\b)/is.test(sql) && confirm?.trim() !== env) {
			await delay(null);
			throw new ApiError(`This can't be undone easily. Type ${env} to run it.`, 409, true);
		}
		if (write && /\b(begin|commit)\b/i.test(sql)) throw new ApiError('Leave out BEGIN and COMMIT; Homelab runs it all in one go.', 400);
		if (write) return delay({ columns: [], rows: [], row_count: 0, truncated: false, command: 'UPDATE 1', ms: 4 });
		if (/^\s*select/i.test(sql)) return delay({ columns: ['id', 'title'], rows: [['1', 'Dune'], ['2', null]], row_count: 2, truncated: false, command: 'SELECT 2', ms: 12 });
		throw new ApiError('syntax error at or near "x"', 400);
	},
	async dbChanges(_app, _env, before, limit = 50) {
		return delay(dbChangeStore.filter((c) => !before || c.id < before).slice(0, limit).map((c) => ({ ...c })));
	},
	async importDb(_id, _env, file, onProgress) {
		for (let i = 1; i <= 5; i++) {
			await delay(null, 150);
			onProgress?.(i / 5);
		}
		return /\.(db|sqlite3?)$/i.test(file.name) ? 'sqlite' : /\.dump$/i.test(file.name) ? 'pg_dump' : 'sql';
	},
	appDatabase: () => delay({ engine: 'postgres', envs: [{ env: 'dev', port: 17005, user: 'app', database: 'app', copies: dbCopies.dev.map((c) => ({ ...c })) }, { env: 'prod', port: 17015, user: 'app', database: 'app', copies: dbCopies.prod.map((c) => ({ ...c })) }] }),
	async makeDbCopy(_id, env) {
		dbCopies[env].unshift({ name: `${Date.now()}-manual`, made: new Date().toISOString(), size_bytes: 2_100_000, kind: 'manual' });
		await delay(null);
	},
	restoreDbCopy: async () => void (await delay(null)),
	copyProdToDev: async () => void (await delay(null))
};
