<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { ActivityEvent } from '#lib/types.ts';
	import EventList from '#lib/components/EventList.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	const PAGE = 50;
	let events = $state<ActivityEvent[]>([]);
	let loaded = $state(false);
	let more = $state(false);
	let loadingMore = $state(false);
	let error = $state('');

	async function load(before?: number) {
		error = '';
		try {
			const rows = await api.activity({ before, limit: PAGE });
			events = before ? [...events, ...rows] : rows;
			more = rows.length === PAGE;
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
			loadingMore = false;
		}
	}

	onMount(() => {
		load();
		if (!appsStore.loaded) appsStore.refresh();
	});

	const appName = (id: string) => appsStore.get(id)?.name ?? id;
</script>

<svelte:head><title>Activity · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head">
		<h1>Activity</h1>
		<button class="btn small" type="button" onclick={() => load()}>Refresh</button>
	</div>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if events.length === 0 && !error}
		<div class="card"><p class="muted">Nothing has happened yet. Deploys, promotes and roll backs will show up here.</p></div>
	{:else}
		<div class="card"><EventList {events} {appName} /></div>
		{#if more}
			<button class="btn" type="button" disabled={loadingMore} onclick={() => ((loadingMore = true), load(events[events.length - 1].id))}>
				{loadingMore ? 'Loading…' : 'Load more'}
			</button>
		{/if}
	{/if}
</div>

<style>
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
</style>
