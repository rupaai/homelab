<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { startPolling } from '#lib/poll.ts';
	import type { ActivityEvent, EnvName } from '#lib/types.ts';
	import DeploySheet from '#lib/components/DeploySheet.svelte';
	import EnvCard from '#lib/components/EnvCard.svelte';
	import EventList from '#lib/components/EventList.svelte';
	import PromoteSheet from '#lib/components/PromoteSheet.svelte';
	import RollbackSheet from '#lib/components/RollbackSheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	const id = $derived(page.params.id ?? '');
	const app = $derived(appsStore.get(id));

	let sheet = $state<null | { kind: 'promote' | 'deploy' } | { kind: 'rollback'; env: EnvName }>(null);
	let events = $state<ActivityEvent[]>([]);
	let eventsLoaded = $state(false);

	async function loadEvents() {
		try {
			events = await api.activity({ app: id, limit: 10 });
		} catch {
			// the list just stays as it was
		} finally {
			eventsLoaded = true;
		}
	}

	onMount(() => {
		const stop = startPolling(async () => {
			await appsStore.refresh();
			await loadEvents();
		}, () => appsStore.pollMs);
		return stop;
	});

	// After a change: close the sheet and refresh right away so progress shows up.
	function started() {
		sheet = null;
		appsStore.refresh();
		loadEvents();
	}
</script>

<svelte:head><title>{app?.name ?? 'App'} · Homehub</title></svelte:head>

<div class="page">
	<a class="back" href="/">← Projects</a>

	{#if !appsStore.loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if !app}
		<div class="card">
			<p><strong>There's no app called "{id}".</strong></p>
			<p class="muted">It may have been removed. <a href="/">Back to your projects</a>.</p>
		</div>
	{:else}
		<div class="head">
			<span class="icon" aria-hidden="true">{app.icon || app.name[0]?.toUpperCase()}</span>
			<div>
				<h1>{app.name}</h1>
				<p class="muted small"><a href={app.repoUrl} target="_blank" rel="noopener">{app.repo}<span class="sr-only"> (opens in a new tab)</span></a></p>
			</div>
		</div>

		{#if app.latestBuild?.status === 'failed'}
			<p class="notice bad">
				<strong>The latest build failed</strong>
				Build {app.latestBuild.short} won't be deployed. <a href={app.latestBuild.url} target="_blank" rel="noopener">See what went wrong<span class="sr-only"> (opens in a new tab)</span></a>
			</p>
		{:else if app.latestBuild?.status === 'running'}
			<p class="notice info"><Spinner /> A new build ({app.latestBuild.short}) is being made.</p>
		{/if}

		{#each app.envs as env (env.env)}
			<EnvCard
				{app}
				{env}
				onpromote={() => (sheet = { kind: 'promote' })}
				onrollback={() => (sheet = { kind: 'rollback', env: env.env })}
				ondeploy={() => (sheet = { kind: 'deploy' })}
			/>
		{/each}

		<section aria-labelledby="recent">
			<div class="page-head">
				<h2 id="recent">Recent activity</h2>
				<a class="small" href="/activity">All activity</a>
			</div>
			<div class="card">
				{#if events.length}
					<EventList {events} showApp={false} />
				{:else if eventsLoaded}
					<p class="muted">Nothing has happened yet.</p>
				{:else}
					<p class="muted"><Spinner /> Loading…</p>
				{/if}
			</div>
		</section>

		{#if sheet?.kind === 'promote'}
			<PromoteSheet appId={app.id} onclose={() => (sheet = null)} ondone={started} />
		{:else if sheet?.kind === 'rollback'}
			<RollbackSheet appId={app.id} env={sheet.env} onclose={() => (sheet = null)} ondone={started} />
		{:else if sheet?.kind === 'deploy'}
			<DeploySheet {app} onclose={() => (sheet = null)} ondone={started} />
		{/if}
	{/if}
</div>

<style>
	.back {
		text-decoration: none;
		font-weight: 600;
		align-self: flex-start;
		padding: 0.25rem 0;
	}
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.head p {
		margin: 0;
	}
	.icon {
		width: 3rem;
		height: 3rem;
		border-radius: 14px;
		background: var(--accent-soft);
		color: var(--accent);
		display: grid;
		place-items: center;
		font-size: 1.6rem;
		font-weight: 700;
	}
	.notice.info {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		border-color: var(--border-strong);
		background: var(--surface-2);
	}
	h2 {
		margin: 0;
	}
	section .page-head {
		margin-bottom: 0.5rem;
	}
</style>
