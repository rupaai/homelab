<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { messageOf } from '#lib/errors.ts';
	import { appInitial, envLabel } from '#lib/format.ts';
	import { startPolling } from '#lib/poll.ts';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';
	import StatusDot from '#lib/components/StatusDot.svelte';

	onMount(() => startPolling(appsStore.refresh, () => appsStore.pollMs));

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
			await appsStore.refresh();
			adding = false;
			goto(`/apps/${encodeURIComponent(app.id)}`);
		} catch (err) {
			addError = messageOf(err);
		} finally {
			sending = false;
		}
	}
</script>

<svelte:head><title>Projects · Homehub</title></svelte:head>

<div class="page">
	<div class="page-head">
		<h1>Projects</h1>
		<button class="btn primary small" type="button" onclick={() => ((adding = true), (addError = ''))}>Add app</button>
	</div>

	{#if !appsStore.loaded}
		<p class="muted loading"><Spinner /> Loading your apps…</p>
	{:else if appsStore.error && appsStore.list.length === 0}
		<p class="notice bad" role="alert">{appsStore.error}</p>
	{:else if appsStore.list.length === 0}
		<div class="card empty">
			<p><strong>No apps yet.</strong></p>
			<p class="muted">Add an app from its GitHub repo and Homehub will show how dev and prod are doing.</p>
		</div>
	{/if}

	{#each appsStore.list as app (app.id)}
		{@const b = app.latestBuild}
		<article class="card app">
			<div class="head">
				<span class="icon" aria-hidden="true">{app.icon || appInitial(app.name)}</span>
				<div class="titles">
					<h2><a class="stretch" href="/apps/{encodeURIComponent(app.id)}">{app.name}</a></h2>
					<p class="repo muted small">{app.repo}</p>
				</div>
				<svg class="chev" viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
			</div>

			<ul class="envs">
				{#each app.envs as env (env.env)}
					<li>
						<span class="envname">{envLabel(env.env)}</span>
						<span class="state">
							{#if env.busy}
								<span class="busy"><Spinner /> {env.busy}…</span>
							{:else}
								<StatusDot health={env.health} />
							{/if}
						</span>
						<code class="sha">{env.version ? env.version.short : '—'}</code>
					</li>
				{/each}
			</ul>

			{#if b?.status === 'failed'}
				<p class="hint-row bad"><a href={b.url} target="_blank" rel="noopener">Build failed<span class="sr-only"> (opens in a new tab)</span></a> <span class="muted">for {b.short}, so it won't be deployed</span></p>
			{:else if b?.status === 'passed' && !b.deployed}
				<p class="hint-row ready"><strong>New build ready</strong> <span class="muted">({b.short}), not on dev yet</span></p>
			{/if}
		</article>
	{/each}
</div>

{#if adding}
	<Sheet title="Add an app" onclose={() => (adding = false)}>
		{#snippet children()}
			<form id="add-form" onsubmit={add}>
				<label for="repo">GitHub repo</label>
				<input id="repo" type="text" bind:value={repo} placeholder="owner/name" autocapitalize="none" autocorrect="off" spellcheck="false" required />
				<p class="hint">Like <code>rupaai/movie-night</code>. The repo needs a <code>homehub.yaml</code> file.</p>
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
		align-items: center;
		gap: 0.75rem;
	}
	.icon {
		width: 2.6rem;
		height: 2.6rem;
		border-radius: 12px;
		background: var(--accent-soft);
		color: var(--accent);
		display: grid;
		place-items: center;
		font-size: 1.4rem;
		font-weight: 700;
		flex: none;
	}
	.titles {
		min-width: 0;
		flex: 1;
	}
	h2 {
		margin: 0;
		font-size: 1.1rem;
	}
	h2 a {
		color: var(--text);
		text-decoration: none;
	}
	/* The whole card is the link; the build links below sit above it. */
	.stretch::after {
		content: '';
		position: absolute;
		inset: 0;
		border-radius: var(--radius);
	}
	.repo {
		margin: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.chev {
		color: var(--muted);
		flex: none;
	}
	.envs {
		list-style: none;
		margin: 0.7rem 0 0;
		padding: 0;
		border-top: 1px solid var(--border);
	}
	.envs li {
		display: grid;
		grid-template-columns: 3.2rem 1fr auto;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem 0 0;
	}
	.envname {
		font-weight: 600;
		color: var(--muted);
		font-size: 0.875rem;
	}
	.busy {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-weight: 600;
		color: var(--accent);
	}
	.sha {
		font-size: 0.85rem;
		background: var(--chip);
		padding: 0.05rem 0.4rem;
		border-radius: 6px;
	}
	.hint-row {
		position: relative;
		z-index: 1;
		margin: 0.7rem 0 0;
		padding: 0.45rem 0.7rem;
		border-radius: 10px;
		font-size: 0.9rem;
	}
	.ready {
		background: var(--accent-soft);
	}
	.bad {
		background: var(--bad-bg);
	}
	.hint-row a {
		font-weight: 700;
		color: var(--bad);
	}
</style>
