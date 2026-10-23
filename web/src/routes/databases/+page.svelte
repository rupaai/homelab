<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { bytes } from '#lib/format.ts';
	import { dbStatusWords, tableCount } from '#lib/databases.ts';
	import type { DbInfo } from '#lib/types.ts';
	import Spinner from '#lib/components/Spinner.svelte';

	let dbs = $state<DbInfo[]>([]);
	let loaded = $state(false);
	let error = $state('');

	onMount(async () => {
		try {
			dbs = await api.databases();
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
	});
</script>

<svelte:head><title>Databases · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><h1>Databases</h1></div>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if !error}
		<section class="card" aria-labelledby="dbs-h">
			<h2 id="dbs-h">Databases</h2>
			{#if dbs.length === 0}
				<p class="muted none">No databases yet. Add <code>database: postgres</code> to an app's homelab.yaml.</p>
			{:else}
				<ul>
					{#each dbs as d (d.app + '/' + d.env)}
						<li>
							<a class="db" href="/databases/{encodeURIComponent(d.app)}/{encodeURIComponent(d.env)}">
								<strong>{d.app} · {d.env}</strong>
								<span class="muted small">{d.engine} · {dbStatusWords(d.status)} · {bytes(d.size_bytes)} · {tableCount(d.tables)}</span>
							</a>
						</li>
					{/each}
				</ul>
			{/if}
		</section>
	{/if}
</div>

<style>
	.loading {
		margin: 0;
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	h2 {
		margin: 0 0 0.4rem;
		font-size: 1.1rem;
	}
	.none {
		margin: 0.25rem 0;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		padding: 0.6rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	.db {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		text-decoration: none;
		color: var(--text);
		overflow-wrap: anywhere;
	}
</style>
