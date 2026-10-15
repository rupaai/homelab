<script lang="ts">
	import { onMount } from 'svelte';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { startPolling } from '#lib/poll.ts';
	import { appInitial } from '#lib/format.ts';
	import Spinner from '#lib/components/Spinner.svelte';

	onMount(() => {
		void appsStore.refresh();
		return startPolling(appsStore.refresh, () => appsStore.pollMs);
	});
</script>

<svelte:head><title>Apps · Homelab</title></svelte:head>

<div class="page">
	<h1>Apps</h1>
	<p class="muted">Open the apps you have access to.</p>
	{#if !appsStore.loaded}
		<p class="muted loading"><Spinner /> Loading your apps…</p>
	{:else if appsStore.list.length === 0}
		<div class="card"><p><strong>No apps are shared with you yet.</strong></p><p class="muted">Ask a Homelab admin to give you access.</p></div>
	{/if}
	{#each appsStore.list as app (app.id)}
		{@const prod = app.envs.find((e) => e.env === 'prod')}
		<article class="card app-card">
			<div class="app-icon" aria-hidden="true">{app.icon || appInitial(app.name)}</div>
			<div class="app-details"><h2>{app.name}</h2><p class="muted">Production</p></div>
			{#if prod?.address && prod.addressReady}
				<a class="btn primary small" href={prod.address} target="_blank" rel="noopener">Open<span class="sr-only"> {app.name}</span></a>
			{:else}
				<span class="muted small">Not running</span>
			{/if}
		</article>
	{/each}
</div>

<style>
	.loading { display: flex; align-items: center; gap: .5rem; }
	.app-card { display:flex; align-items:center; gap:.75rem; }
	.app-icon { width:2.6rem;height:2.6rem;display:grid;place-items:center;border-radius:12px;background:var(--accent-soft);color:var(--accent);font-size:1.3rem;font-weight:700; }
	.app-details { flex:1;min-width:0; }
	.app-details h2,.app-details p { margin:0; }
</style>
