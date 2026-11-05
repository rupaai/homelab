<script lang="ts">
	import { logsHref as searchHref } from '#lib/logs.ts';
	import { appAddress, deployedLabel, envLabel, healthSentence } from '#lib/format.ts';
	import type { AppView, EnvView } from '#lib/types.ts';
	import ReleaseLine from './ReleaseLine.svelte';
	import Spinner from './Spinner.svelte';
	import StatusDot from './StatusDot.svelte';
	import Time from './Time.svelte';

	let {
		app,
		env,
		onpromote,
		onrollback,
		onrestart,
		onturnoff,
		onturnon,
		ondeploy,
		admin = false
	}: {
		app: AppView;
		env: EnvView;
		onpromote: () => void;
		onrollback: () => void;
		onrestart: () => void;
		onturnoff: () => void;
		onturnon: () => void;
		ondeploy: () => void;
		admin?: boolean;
	} = $props();

	const build = $derived(app.latestBuild);
	const canDeploy = $derived(env.env === 'dev' && !env.off && build?.status === 'passed' && !build.deployed);
	const canRestart = $derived(admin && !env.off && !!env.version && env.state !== '');
	const canTurnOff = $derived(admin && env.env === 'dev' && !env.off);
	const logsHref = $derived(`/apps/${encodeURIComponent(app.id)}/logs/${env.env}`);
</script>

<section class="card" aria-labelledby="env-{env.env}">
	<div class="top">
		<h2 id="env-{env.env}">{envLabel(env.env)}</h2>
		<StatusDot health={env.health} label={env.off ? 'Off' : undefined} />
	</div>

	<p class="address">
		{#if env.off}
			<span class="muted">No address while dev is off</span>
		{:else if env.addressReady && env.address}
			<a href={appAddress(env.address)} target="_blank" rel="noopener">{appAddress(env.address).replace(/^https?:\/\//, '')}<span class="sr-only"> (opens in a new tab)</span></a>
		{:else}
			<span class="muted">Address not ready yet</span>
		{/if}
	</p>

	{#if env.busy}
		<p class="busy" role="status"><Spinner /> {env.busy}…</p>
	{/if}

	{#if env.off}
		<p class="status-line">Dev is off. It is stopped and its data is kept. New builds wait until you turn it on.</p>
	{:else if env.version}
		<p class="status-line">{healthSentence(app.name, env.health)}{env.healthDetail ? `. ${env.healthDetail}` : ''}</p>
	{/if}

	{#if env.missingSecrets.length}
		<p class="notice">
			<strong>{env.missingSecrets.length === 1 ? `${env.missingSecrets[0]} isn't set for ${env.env}` : `${env.missingSecrets.length} secrets aren't set for ${env.env}`}</strong>
			{#if env.missingSecrets.length > 1}{env.missingSecrets.join(', ')}.{/if}
			{#if admin}<a href="/apps/{encodeURIComponent(app.id)}/secrets">Set {env.missingSecrets.length === 1 ? 'it' : 'them'} on the secrets screen</a>{:else}Ask an admin to set {env.missingSecrets.length === 1 ? 'it' : 'them'}.{/if}
		</p>
	{/if}

	{#if env.lastError}
		<p class="notice"><strong>This didn't start</strong>{env.lastError}</p>
	{/if}

	<h3>Version</h3>
	<ReleaseLine release={env.version} />
	{#if env.version && env.deployedAt}
		<p class="muted small by">
			{deployedLabel(env.deployKind)} by {env.deployedBy === 'homelab' ? 'Homelab' : env.deployedBy}, <Time at={env.deployedAt} />
		</p>
	{/if}

	<div class="actions">
		{#if env.env === 'prod'}
			<button class="btn primary" type="button" onclick={onpromote} disabled={!!env.busy}>Promote from dev</button>
		{:else if env.off}
			{#if admin}<button class="btn primary" type="button" onclick={onturnon} disabled={!!env.busy}>Turn on</button>{/if}
		{:else if canDeploy}
			<button class="btn primary" type="button" onclick={ondeploy} disabled={!!env.busy}>Deploy latest build</button>
		{/if}
		<a class="btn" href={searchHref(app.id, env.env)}>Logs</a>
		<details class="more">
			<summary class="btn">More</summary>
			<div class="menu">
				{#if env.canRollback && !env.off}
					<button class="btn" type="button" onclick={onrollback} disabled={!!env.busy}>Roll back</button>
				{/if}
				{#if canRestart}
					<button class="btn" type="button" onclick={onrestart} disabled={!!env.busy}>Restart</button>
				{/if}
				{#if canTurnOff}
					<button class="btn" type="button" onclick={onturnoff} disabled={!!env.busy}>Turn off</button>
				{/if}
				<a class="btn" href={logsHref}>Raw output</a>
			</div>
		</details>
	</div>

	<details class="tech">
		<summary>Technical details</summary>
		<dl>
			<dt>Container</dt>
			<dd>{env.state || 'none'}</dd>
			<dt>Last checked</dt>
			<dd>{#if env.checkedAt}<Time at={env.checkedAt} />{:else}never{/if}</dd>
			{#if env.version}
				<dt>Image</dt>
				<dd><code>{env.version.image}:{env.version.tag}</code></dd>
				<dt>Digest</dt>
				<dd><code>{env.version.digest}</code></dd>
				{#if env.version.buildUrl}
					<dt>Build</dt>
					<dd><a href={env.version.buildUrl} target="_blank" rel="noopener">View the build</a></dd>
				{/if}
			{/if}
		</dl>
	</details>
</section>

<style>
	.top {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
	}
	h2 {
		margin: 0;
	}
	.address {
		margin: 0.2rem 0 0.6rem;
		overflow-wrap: anywhere;
	}
	.busy {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
		background: var(--accent-soft);
		border-radius: 10px;
		padding: 0.55rem 0.8rem;
		margin: 0 0 0.6rem;
	}
	.notice {
		margin: 0 0 0.6rem;
	}
	.status-line {
		margin: 0 0 0.6rem;
	}
	.by {
		margin: 0.4rem 0 0;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 1rem;
	}
	.tech {
		margin-top: 0.8rem;
		font-size: 0.875rem;
	}
	.tech summary {
		cursor: pointer;
		color: var(--muted);
		padding: 0.25rem 0;
	}
	.more summary {
		list-style: none;
	}
	.more summary::-webkit-details-marker {
		display: none;
	}
	.more summary::after {
		content: '▾';
		font-size: 0.8em;
	}
	.more[open] {
		flex-basis: 100%;
	}
	.menu {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.5rem;
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.2rem 0.8rem;
		margin: 0.4rem 0 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
</style>
