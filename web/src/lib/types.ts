// These mirror the JSON from the Homehub API (internal/hub/view.go, actions.go,
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
}

export interface AppView {
	id: string;
	name: string;
	icon: string;
	repo: string;
	repoUrl: string;
	promotion: 'manual' | 'auto-dev' | string;
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

/** Everything the screens need from the server. The mock implements the same shape. */
export interface Api {
	apps(): Promise<AppView[]>;
	addApp(repo: string, branch: string): Promise<AppView>;
	deploy(appId: string, commit?: string): Promise<void>;
	promotePreview(appId: string): Promise<PromotePreview>;
	promote(appId: string): Promise<void>;
	rollbackPreview(appId: string, env: EnvName): Promise<RollbackPreview>;
	rollback(appId: string, env: EnvName): Promise<void>;
	logs(appId: string, env: EnvName, lines: number): Promise<string>;
	activity(q?: ActivityQuery): Promise<ActivityEvent[]>;
}
