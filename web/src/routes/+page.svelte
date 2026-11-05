<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { appAddress, appInitial, envLabel, releaseName } from '#lib/format.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import type { AppView, MeView } from '#lib/types.ts';
	import { startPolling } from '#lib/poll.ts';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';
	import StatusDot from '#lib/components/StatusDot.svelte';
	import { appsSummary, summaryParts } from '#lib/summary.ts';

	let projects = $state<AppView[]>([]);
	let projectsLoaded = $state(false);
	let projectsError = $state('');
	let me = $state<MeView | null>(null);
	const canAddApps = $derived(!!me?.roles.some((role) => role === 'owner' || role === 'admin'));
	const builder = $derived(canAddApps || !!me?.roles.includes('builder'));
	// Builders see project cards. Everyone else sees the apps shared with them.
	let meLoaded = $state(false);
	const summary = $derived(summaryParts(appsSummary(projects)));

	async function refreshProjects() {
		try {
			me = await api.me();
			meLoaded = true;
			if (builder) { projects = await api.projects(); projectsError = ''; projectsLoaded = true; }
			else { await appsStore.refresh(); }
		}
		catch (error) { projectsError = messageOf(error); projectsLoaded = true; }
	}

	onMount(() => startPolling(refreshProjects, () => (builder ? 5000 : appsStore.pollMs)));

	let adding = $state(false);
	let repo = $state('');
	let branch = $state('');
	let addError = $state('');
	let sending = $state(false);

	async function add(e: SubmitEvent) {
		e.preventDefault();
		sending = true;
		addError = '';
		try {
			const app = await api.addApp(repo.trim(), branch.trim());
			await refreshProjects();
			adding = false;
			goto(`/apps/${encodeURIComponent(app.id)}`);
		} catch (err) {
			addError = messageOf(err);
		} finally {
			sending = false;
		}
	}
</script>

<svelte:head><title>Apps · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head">
		<h1>Apps</h1>
		{#if canAddApps}<button class="btn primary" type="button" onclick={() => ((adding = true), (addError = ''))}><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>Add app</button>{/if}
	</div>

	{#if builder && summary.length > 0}
		<p class="summary" aria-label="Summary of your apps">
			{#each summary as part, i}
				{#if i > 0}<span class="sep" aria-hidden="true">·</span>{/if}
				<span class={part.tone}>{part.text}</span>
			{/each}
		</p>
	{/if}

	{#if !meLoaded && !projectsError}
		<p class="muted loading"><Spinner /> Loading your apps…</p>
	{:else if !builder && meLoaded}
		{#if !appsStore.loaded}
			<p class="muted loading"><Spinner /> Loading your apps…</p>
		{:else if appsStore.list.length === 0}
			<div class="card"><p><strong>No apps are shared with you yet.</strong></p><p class="muted">Ask a Homelab admin to give you access.</p></div>
		{/if}
		<div class="grid">
			{#each appsStore.list as app (app.id)}
				{@const prod = app.envs.find((e) => e.env === 'prod')}
				<article class="card family-card">
					<div class="icon" aria-hidden="true">{app.icon || appInitial(app.name)}</div>
					<div class="titles"><h2>{app.name}</h2><p class="muted small repo">Production</p></div>
					{#if prod?.address && prod.addressReady}
						<a class="btn primary small" href={appAddress(prod.address)} target="_blank" rel="noopener">Open<span class="sr-only"> {app.name} (opens in a new tab)</span></a>
					{:else}
						<span class="muted small">Not running</span>
					{/if}
				</article>
			{/each}
		</div>
	{:else if projectsError && projects.length === 0}
		<p class="notice bad" role="alert">{projectsError}</p>
	{:else if projects.length === 0}
		<div class="card empty">
			<p><strong>No apps yet.</strong></p>
			<p class="muted">Add an app from its GitHub repo and Homelab will show how dev and prod are doing.</p>
		</div>
	{/if}

	<div class="grid">
	{#if builder}{#each projects as app (app.id)}
		{@const b = app.latestBuild}
		{@const prod = app.envs.find((e) => e.env === 'prod')}
		<article class="card app">
			<div class="head">
				<span class="icon" aria-hidden="true">{app.icon || appInitial(app.name)}</span>
				<div class="titles">
					<h2><a class="stretch" href="/apps/{encodeURIComponent(app.id)}">{app.name}</a></h2>
					<p class="repo">{app.repo}</p>
				</div>
				{#if prod?.address && prod.addressReady}
					<a class="open" href={appAddress(prod.address)} target="_blank" rel="noopener">Open<span class="sr-only"> {app.name} (opens in a new tab)</span></a>
				{/if}
			</div>

			<ul class="envs">
				{#each app.envs as env (env.env)}
					<li>
						<span class="envname">{envLabel(env.env)}</span>
						<span class="state">
							{#if env.busy}
								<span class="busy"><Spinner /> {env.busy}…</span>
							{:else}
								<StatusDot health={env.health} label={!env.version && env.health === 'unknown' ? 'Not deployed' : undefined} />
							{/if}
						</span>
						<code class="sha">{env.version ? releaseName(env.version) : '—'}</code>
					</li>
				{/each}
			</ul>

			{#if b?.status === 'failed'}
				<p class="note bad"><a href={b.url} target="_blank" rel="noopener">Build failed<span class="sr-only"> (opens in a new tab)</span></a> for {b.short}, so it won't be deployed</p>
			{:else if b?.status === 'passed' && !b.deployed}
				<p class="note"><strong>New build ready</strong> ({b.short}), not on dev yet</p>
			{/if}
		</article>
	{/each}{/if}
	</div>
</div>

{#if adding}
	<Sheet title="Add an app" onclose={() => (adding = false)}>
		{#snippet children()}
			<form id="add-form" onsubmit={add}>
				<label for="repo">GitHub repo</label>
				<input id="repo" type="text" bind:value={repo} placeholder="owner/name" autocapitalize="none" autocorrect="off" spellcheck="false" required />
				<p class="hint">Like <code>rupaai/movie-night</code>. The repo needs a <code>homelab.yaml</code> file.</p>
				<label for="branch">Branch <span class="muted">(optional)</span></label>
				<input id="branch" type="text" bind:value={branch} placeholder="main" autocapitalize="none" autocorrect="off" spellcheck="false" />
				{#if addError}<p class="notice bad" role="alert" style="margin-top: 1rem">{addError}</p>{/if}
			</form>
		{/snippet}
		{#snippet footer()}
			<button class="btn" type="button" onclick={() => (adding = false)}>Cancel</button>
			<button class="btn primary" type="submit" form="add-form" disabled={sending || !repo.trim()}>{sending ? 'Adding…' : 'Add app'}</button>
		{/snippet}
	</Sheet>
{/if}

<style>
	.grid {
		display: grid;
		gap: 1rem;
		grid-template-columns: minmax(0, 1fr);
	}
	@media (min-width: 720px) {
		.grid {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			align-items: start;
		}
	}
	.summary {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.5rem;
		margin: 0;
		padding: 0.75rem 1rem;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		font-size: 0.875rem;
		color: var(--muted);
	}
	.summary .sep {
		color: var(--border-strong);
	}
	.summary .warn {
		color: var(--warn);
		font-weight: 600;
	}
	.summary .bad {
		color: var(--bad);
		font-weight: 600;
	}
	.family-card {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.family-card h2 {
		margin: 0;
	}
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.empty p {
		margin: 0.2rem 0;
	}
	.app {
		position: relative;
	}
	.head {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
	}
	.icon {
		width: 2.5rem;
		height: 2.5rem;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--surface-2);
		color: var(--accent);
		display: grid;
		place-items: center;
		font-size: 1.25rem;
		font-weight: 600;
		flex: none;
	}
	.titles {
		min-width: 0;
		flex: 1;
	}
	h2 {
		margin: 0;
		font-size: 1.0625rem;
		line-height: 1.5rem;
	}
	h2 a {
		color: var(--text);
		text-decoration: none;
	}
	/* The whole card is the link; the Open button and build links sit above it. */
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: var(--radius);
	}
	.repo {
		margin: 0;
		font: 0.8125rem/1.25rem var(--mono);
		color: var(--muted);
		overflow-wrap: anywhere;
	}
	.open {
		position: relative;
		z-index: 1;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 44px;
		margin: -0.375rem 0;
		padding: 0 1rem;
		border: 1px solid var(--border-strong);
		border-radius: 8px;
		color: var(--text);
		font-size: 0.875rem;
		font-weight: 500;
		text-decoration: none;
		flex: none;
	}
	.open:hover {
		background: var(--surface-2);
	}
	.envs {
		list-style: none;
		margin: 1rem 0 0;
		padding: 0;
		display: grid;
		gap: 0.5rem;
	}
	.envs li {
		display: grid;
		grid-template-columns: 3rem minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.75rem;
		min-height: 44px;
		padding: 0 0.75rem;
		background: var(--surface-2);
		border-radius: 8px;
	}
	.envname {
		color: var(--muted);
		font-size: 0.8125rem;
	}
	.busy {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--accent);
	}
	.sha {
		font-size: 0.8125rem;
		color: var(--text);
	}
	.note {
		position: relative;
		z-index: 1;
		margin: 0.75rem 0 0;
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.note strong {
		font-weight: 500;
		color: var(--text);
	}
	.note.bad,
	.note.bad a {
		color: var(--bad);
	}
</style>
