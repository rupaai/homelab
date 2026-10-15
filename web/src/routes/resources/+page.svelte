<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { bytes } from '#lib/format.ts';
	import type { ResourcesView } from '#lib/types.ts';
	import Spinner from '#lib/components/Spinner.svelte';

	let view = $state<ResourcesView | null>(null);
	let error = $state('');
	let busy = $state(false);

	async function load() {
		busy = true;
		try { view = await api.resources(); error = ''; }
		catch (e) { error = messageOf(e); }
		finally { busy = false; }
	}
	onMount(load);

	const pct = (used: number, total: number) => (total > 0 ? Math.round((used / total) * 100) : 0);
</script>

<svelte:head><title>Resources · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head">
		<div><h1>Resources</h1><p class="muted">What this computer and each app use right now.</p></div>
		<button class="btn small" type="button" disabled={busy} onclick={load}>{busy ? 'Checking…' : 'Refresh'}</button>
	</div>
	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{#if !view && !error}<p class="muted loading"><Spinner /> Checking…</p>{/if}
	{#if view}
		{#each view.problems ?? [] as p}<p class="notice">{p}</p>{/each}
		{#if view.device}
			{@const d = view.device}
			<section class="card" aria-labelledby="device">
				<h2 id="device">This computer</h2>
				<div class="meter"><span>Processor</span><strong>{Math.round(d.cpuPercent)}%</strong><progress max="100" value={d.cpuPercent} aria-label="Processor use"></progress></div>
				<div class="meter"><span>Memory</span><strong>{bytes(d.memUsed)} of {bytes(d.memTotal)}</strong><progress max="100" value={pct(d.memUsed, d.memTotal)} aria-label="Memory use"></progress></div>
				<div class="meter" class:warn={pct(d.diskUsed, d.diskTotal) >= 85}><span>Disk</span><strong>{bytes(d.diskUsed)} of {bytes(d.diskTotal)} ({pct(d.diskUsed, d.diskTotal)}%)</strong><progress max="100" value={pct(d.diskUsed, d.diskTotal)} aria-label="Disk use"></progress></div>
				{#if pct(d.diskUsed, d.diskTotal) >= 85}<p class="notice">The disk is almost full. Homelab sends admins a notification above 85%.</p>{/if}
			</section>
		{/if}
		<section class="card" aria-labelledby="apps">
			<h2 id="apps">Apps</h2>
			{#if view.apps.length === 0}<p class="muted">No app is running.</p>{:else}
				<table>
					<thead><tr><th scope="col">App</th><th scope="col">Processor</th><th scope="col">Memory</th></tr></thead>
					<tbody>
						{#each view.apps as a (a.app + a.env)}
							<tr><td><a href="/apps/{a.app}">{a.name}</a>{#if a.env !== 'prod'} <span class="chip">{a.env}</span>{/if}</td><td>{a.cpuPercent.toFixed(1)}%</td><td>{bytes(a.memUsed)}</td></tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</section>
	{/if}
</div>

<style>
	.page-head p { margin: 0.35rem 0 0; }
	.loading { display: flex; align-items: center; gap: 0.5rem; }
	.card + .card, .notice + .card { margin-top: 1rem; }
	h2 { font-size: 1.05rem; margin: 0 0 0.6rem; }
	.meter { display: grid; grid-template-columns: 1fr auto; gap: 0.2rem 0.75rem; margin: 0 0 0.8rem; }
	.meter progress { grid-column: 1 / -1; width: 100%; height: 0.5rem; accent-color: var(--accent); }
	.meter.warn progress { accent-color: var(--bad-border); }
	table { width: 100%; border-collapse: collapse; }
	th, td { text-align: left; padding: 0.45rem 0.25rem; border-bottom: 1px solid var(--border); }
	th { font-size: 0.85rem; color: var(--muted); font-weight: 600; }
	td:not(:first-child), th:not(:first-child) { text-align: right; white-space: nowrap; }
</style>
