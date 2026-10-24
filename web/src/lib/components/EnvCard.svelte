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
		ondeploy,
		admin = false
	}: { app: AppView; env: EnvView; onpromote: () => void; onrollback: () => void; ondeploy: () => void; admin?: boolean } = $props();

	const build = $derived(app.latestBuild);
	const canDeploy = $derived(env.env === 'dev' && build?.status === 'passed' && !build.deployed);
	const logsHref = $derived(`/apps/${encodeURIComponent(app.id)}/logs/${env.env}`);
</script>

<section class="card" aria-labelledby="env-{env.env}">
	<div class="top">
		<h2 id="env-{env.env}">{envLabel(env.env)}</h2>
		<StatusDot health={env.health} />
	</div>

	<p class="address">
		{#if env.addressReady && env.address}
			<a href={appAddress(env.address)} target="_blank" rel="noopener">{appAddress(env.address).replace(/^https?:\/\//, '')}<span class="sr-only"> (opens in a new tab)</span></a>
		{:else}
			<span class="muted">Address not ready yet</span>
		{/if}
	</p>

	{#if env.busy}
		<p class="busy" role="status"><Spinner /> {env.busy}…</p>
	{/if}

	{#if env.version}
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
		{:else if canDeploy}
			<button class="btn primary" type="button" onclick={ondeploy} disabled={!!env.busy}>Deploy latest build</button>
		{/if}
		{#if env.canRollback}
			<button class="btn" type="button" onclick={onrollback} disabled={!!env.busy}>Roll back</button>
		{/if}
		<a class="btn" href={searchHref(app.id, env.env)}>Logs</a>
		<a class="btn" href={logsHref}>Raw output</a>
	</div>

	<details>
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
	details {
		margin-top: 0.8rem;
		font-size: 0.875rem;
	}
	summary {
		cursor: pointer;
		color: var(--muted);
		padding: 0.25rem 0;
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
