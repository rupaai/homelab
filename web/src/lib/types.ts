// These mirror the JSON from the Homelab API (internal/hub/view.go, actions.go,
// internal/state/store.go, internal/core/types.go). Times are RFC 3339 strings.
// Go slices can arrive as null, so the api module turns those into [].

export type Health = 'healthy' | 'degraded' | 'down' | 'unknown';
export type EnvName = 'dev' | 'prod';
export type DeployKind = 'deploy' | 'promote' | 'rollback';

export interface Release {
	commit: string;
	image: string;
	tag: string;
	digest: string;
	message: string;
	author: string;
	when: string;
	buildUrl: string;
}

export interface ReleaseView extends Release {
	short: string;
	commitUrl: string;
}

export interface BuildView {
	commit: string;
	short: string;
	status: 'passed' | 'failed' | 'running';
	url: string;
	deployed: boolean;
}

export interface EnvView {
	env: EnvName;
	address: string;
	addressName: string;
	addressReady: boolean;
	version: ReleaseView | null;
	state: string;
	health: Health;
	healthDetail: string;
	healthSince: string | null;
	checkedAt: string | null;
	busy: string;
	lastError: string;
	deployedBy: string;
	deployedAt: string | null;
	deployKind: DeployKind | '';
	canRollback: boolean;
	missingSecrets: string[];
}

export type PromotionMode = 'manual' | 'auto-dev' | 'auto';

export interface AutoPromote {
	state: 'waiting' | 'smoke-testing' | 'held' | 'blocked' | 'done' | string;
	at?: string;
	detail: string;
}

export interface HeldCommit {
	commit: string;
	short: string;
	reason: string;
	at: string;
}

export interface MirrorStatus {
	path: string;
	lastSync?: string;
	error?: string;
}

export interface SecretView {
	name: string;
	declared: boolean;
	set: boolean;
	updatedAt?: string;
	updatedBy?: string;
	version?: number;
	generated?: boolean;
}

/** A secret the app saved for itself while it runs. Never has its value. */
export interface SavedSecretView {
	name: string;
	version: number;
	updatedAt: string;
}

export interface EnvSecrets {
	env: EnvName;
	secrets: SecretView[];
	/** "Saved by the app": made by the app, not set by a person. */
	saved: SavedSecretView[];
}

/** An app's secrets. Never has values; canSeeValues says whether this person may ask for them. */
export interface AppSecretsView {
	envs: EnvSecrets[];
	canSeeValues: boolean;
}

/** One kept value of a secret: who set it and when, never the value. */
export interface SecretVersion {
	version: number;
	setBy: string;
	setAt: string;
}

export interface SettingView {
	name: string;
	description: string;
	set: boolean;
	updatedAt?: string;
	updatedBy?: string;
	version?: number;
	appliesAfterRestart: boolean;
}

export interface AppView {
	id: string;
	name: string;
	icon: string;
	repo: string;
	repoUrl: string;
	promotion: PromotionMode | string;
	promoteAfter: string;
	autoPromote: AutoPromote | null;
	held: HeldCommit[];
	mirror: MirrorStatus | null;
	envs: EnvView[];
	latestBuild: BuildView | null;
}

export interface Commit {
	sha: string;
	message: string;
	author: string;
	when: string;
	url: string;
}

export interface PromotePreview {
	from: ReleaseView | null;
	to: ReleaseView | null;
	commits: Commit[];
	compareUrl: string;
	dataCopy: boolean;
	missingSecrets: string[];
	devHealth: Health;
	blockers: string[];
}

export interface RollbackPreview {
	from: ReleaseView | null;
	to: ReleaseView | null;
	blockers: string[];
	note: string;
}

export interface ActivityEvent {
	id: number;
	at: string;
	actor: string;
	app?: string;
	env?: EnvName | '';
	action: string;
	detail: string;
}

export interface ActivityQuery {
	app?: string;
	before?: number;
	limit?: number;
}

export type HouseholdRole = 'owner' | 'admin' | 'builder' | 'member' | string;
export type PersonStatus = 'invited' | 'signed_in' | 'connected' | 'removed' | string;
export interface PersonView {
	id: string;
	name: string;
	email: string;
	identityId?: string;
	status: PersonStatus;
	roles: HouseholdRole[];
	grants: Record<string, 'use' | 'build'>;
	lastSignIn?: string;
}
export interface PersonInput {
	name: string;
	email: string;
	roles: HouseholdRole[];
	grants: Record<string, 'use' | 'build'>;
}
export interface Invitation {
	id?: string;
	url: string;
	/** Homelab emailed the link */
	emailed: boolean;
	/** why it didn't, in plain words */
	emailError?: string;
}
export interface MeView { personId: string; login: string; name: string; local: boolean; roles: HouseholdRole[]; /** may see and change secret values */ canSeeSecrets?: boolean; }
export interface BootstrapView { needsSetup: boolean; signInReady: boolean; ownerNeedsPasskey?: boolean; /** addresses: ip, from another device: sign in with the password */ password?: boolean; }
export interface SetupResult { loginUrl: string; recoveryCode: string; }
export interface PasskeyLink { url: string; }
export interface RemovalStep { name: string; done: boolean; detail?: string; }

/** Everything the screens need from the server. The mock implements the same shape. */
export interface Api {
	bootstrap(): Promise<BootstrapView>;
	initialOwnerPasskeyLink(): Promise<PasskeyLink>;
	me(): Promise<MeView>;
	passkeyLink(): Promise<PasskeyLink>;
	setupOwner(name: string, email: string): Promise<SetupResult>;
	recover(code: string): Promise<void>;
	passwordSignIn(password: string, email?: string): Promise<void>;
	signOut(): Promise<void>;
	people(): Promise<PersonView[]>;
	invitePerson(person: PersonInput): Promise<Invitation>;
	redeemInvite(token: string): Promise<PasskeyLink>;
	/** On another device (addresses: ip): choose a password, which signs the person in. */
	setInvitePassword(token: string, password: string): Promise<void>;
	resendInvite(id: string): Promise<Invitation>;
	testEmail(): Promise<void>;
	updatePerson(id: string, person: PersonInput): Promise<PersonView>;
	removePerson(id: string): Promise<RemovalStep[]>;
	pushKey(): Promise<string>;
	subscribePush(sub: PushSubscriptionInput): Promise<void>;
	unsubscribePush(endpoint: string): Promise<void>;
	testPush(): Promise<number>;
	resources(): Promise<ResourcesView>;
	device(): Promise<DeviceView>;
	updateHomelab(): Promise<string>;
	testVersions(): Promise<TestVersion[]>;
	tryTestVersion(version: string): Promise<TestVersion>;
	endTest(keep: boolean): Promise<string>;
	apps(): Promise<AppView[]>;
	projects(): Promise<AppView[]>;
	addApp(repo: string, branch: string): Promise<AppView>;
	deploy(appId: string, commit?: string): Promise<void>;
	promotePreview(appId: string): Promise<PromotePreview>;
	promote(appId: string): Promise<void>;
	rollbackPreview(appId: string, env: EnvName): Promise<RollbackPreview>;
	rollback(appId: string, env: EnvName): Promise<void>;
	logs(appId: string, env: EnvName, lines: number): Promise<string>;
	activity(q?: ActivityQuery): Promise<ActivityEvent[]>;
	setPromotion(appId: string, mode: PromotionMode): Promise<AppView>;
	removeApp(appId: string): Promise<void>;
	appSecrets(appId: string): Promise<AppSecretsView>;
	revealSecret(appId: string, env: EnvName, name: string): Promise<string>;
	secretVersions(appId: string, env: EnvName, name: string): Promise<SecretVersion[]>;
	restoreSecret(appId: string, env: EnvName, name: string, version: number): Promise<void>;
	setSecret(appId: string, env: EnvName, name: string, value: string): Promise<void>;
	deleteSecret(appId: string, env: EnvName, name: string): Promise<void>;
	regenerateSecret(appId: string, env: EnvName, name: string): Promise<void>;
	revealSavedSecret(appId: string, env: EnvName, name: string): Promise<string>;
	deleteSavedSecret(appId: string, env: EnvName, name: string): Promise<void>;
	settings(): Promise<SettingView[]>;
	setSetting(name: string, value: string): Promise<void>;
	revealSetting(name: string): Promise<string>;
	settingVersions(name: string): Promise<SecretVersion[]>;
	restoreSetting(name: string, version: number): Promise<void>;
	deleteSetting(name: string): Promise<void>;
	keys(): Promise<KeysView>;
	createKey(input: NewKeyInput): Promise<CreatedKey>;
	revokeKey(id: string): Promise<void>;
	storage(): Promise<StorageDisk[]>;
	fileFolders(): Promise<FileFolder[]>;
	createFileFolder(name: string, disk?: string): Promise<FileFolder>;
	/** Only an empty folder can be deleted; the server answers 409 otherwise. */
	deleteFileFolder(name: string): Promise<void>;
	files(folder: string, q?: FileQuery): Promise<FilePage>;
	uploadFile(folder: string, file: File, tags?: string[]): Promise<UploadResult>;
	fileBlob(folder: string, id: string): Promise<Blob>;
	deleteFile(folder: string, id: string): Promise<void>;
	databases(): Promise<DbInfo[]>;
	database(app: string, env: string): Promise<DbInfo>;
	dbTables(app: string, env: string): Promise<DbTable[]>;
	dbRows(app: string, env: string, table: DbTable, offset: number, limit?: number): Promise<DbRows>;
	dbQuery(app: string, env: string, sql: string, write: boolean, confirm?: string): Promise<DbResult>;
	dbChanges(app: string, env: string, before?: number, limit?: number): Promise<DbChange[]>;
	/** Sends the file to replace the database. onProgress gets 0 to 1 while it uploads. */
	importDb(id: string, env: string, file: File, onProgress?: (done: number) => void): Promise<string>;
	appDatabase(id: string): Promise<AppDatabase>;
	makeDbCopy(id: string, env: string): Promise<void>;
	restoreDbCopy(id: string, env: string, name: string): Promise<void>;
	copyProdToDev(id: string): Promise<void>;
}

/** One database of one app environment. connections is null for sqlite. */
export interface DbInfo { app: string; env: string; engine: 'postgres' | 'sqlite'; status: 'ok' | 'down' | 'empty'; size_bytes: number; connections: number | null; tables: number; }
export interface DbTable { schema: string; name: string; rows: number; }
/** Cells are text, or null. */
export interface DbRows { columns: string[]; rows: (string | null)[][]; truncated: boolean; }
export interface DbResult extends DbRows { row_count: number; command: string; ms: number; }
/** One write someone ran from the SQL box. `at` is unix seconds; `copy` is the copy made first, if any. */
export interface DbChange { id: number; person: string; at: number; sql: string; ok: boolean; rows: number; ms: number; error: string; copy: string | null; }
export interface DbCopy { name: string; made: string; size_bytes: number; kind: string; }
export interface DbEnv { env: string; port?: number; user?: string; database?: string; copies: DbCopy[]; }
/** engine is empty when the app has no database. */
export interface AppDatabase { engine: string; envs: DbEnv[]; }

/** A disk Homelab can keep files on. Sizes are bytes. */
export interface StorageDisk { name: string; path: string; kind: 'data' | 'storage' | 'folder'; used: number; total: number; problem?: string; shared?: boolean; }
/** app and env are empty for folders the admins made. created_at is left as the server sends it. */
export interface FileFolder { name: string; disk: string; app: string; env: string; files: number; bytes: number; created_at: string | number; }
export interface StoredFile {
	id: string; folder: string; name: string; content_type: string; size: number; sha256: string;
	created_at: string; created_by: string; tags: string[]; info: Record<string, unknown>;
	meta: FileMeta;
}
/** Facts Homelab read from the file itself. Every key is optional, and the server may add more. */
export interface FileMeta {
	width?: number; height?: number;
	/** RFC 3339 */
	taken_at?: string;
	camera_make?: string; camera_model?: string; lens?: string;
	/** 1 to 8 */
	orientation?: number;
	gps?: { lat: number; lon: number; alt?: number };
	duration_s?: number; video_codec?: string;
	title?: string; artist?: string; album?: string; year?: number;
	pages?: number; author?: string;
}
/** duplicate is true when the folder already had this file, so `file` is the one that was there. */
export interface UploadResult { file: StoredFile; duplicate: boolean; }
export interface FilePage { files: StoredFile[]; next: string | null; }
/** takenAfter and takenBefore are dates (yyyy-mm-dd) or RFC 3339 times. camera is text to look for. */
export interface FileQuery { tag?: string; type?: string; after?: string; limit?: number; takenAfter?: string; takenBefore?: string; camera?: string; }

export interface KeyView {
	id: string; prefix: string; name: string; kind: 'app' | 'admin';
	app?: string; env?: string; scopes: string[];
	createdBy?: string; createdByName?: string; createdAt: string;
	lastUsed?: string; expires?: string; revoked?: boolean;
}
export interface KeysView { admin: KeyView[]; app: KeyView[]; }
export interface NewKeyInput { name: string; scopes: string[]; expires?: string | null; }
export interface CreatedKey { key: string; info: KeyView; }

export interface PushSubscriptionInput { endpoint: string; p256dh: string; auth: string; device: string; }
export interface DeviceUsage { cpuPercent: number; memUsed: number; memTotal: number; diskUsed: number; diskTotal: number; }
export interface AppUsage { app: string; env: EnvName; name: string; cpuPercent: number; memUsed: number; }
export interface ResourcesView { device?: DeviceUsage; apps: AppUsage[]; problems?: string[]; }
export interface DiskInfo { mount: string; device: string; used: number; total: number; health?: 'ok' | 'failing' | ''; }
export interface SystemInfo {
	hostname?: string; os?: string; kernel?: string; uptimeSeconds?: number; temperatureC?: number;
	disks: DiskInfo[]; rebootRequired: boolean;
	/** -1 when unknown */
	updatesPending: number; securityPending: number; reportedAt?: string;
}
export interface UpdateStatus { managed: boolean; current: string; latest?: string; available: boolean; checkedAt?: string; lastResult?: string; note?: string; testing?: TestRun; }
/** A build of an open Homelab pull request. */
export interface TestVersion { version: string; pr: number; title: string; }
export interface TestRun extends TestVersion { from: string; at: string; }
export interface DeviceView { system?: SystemInfo; update?: UpdateStatus; problems?: string[]; }
