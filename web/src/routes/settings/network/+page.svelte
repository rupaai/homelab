<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { groupPorts, isOpen, openSummary, portText, reachWords, serviceWords } from '#lib/network.ts';
	import type { PortRow } from '#lib/types.ts';
	import Spinner from '#lib/components/Spinner.svelte';

	let rows = $state<PortRow[]>([]);
	let loaded = $state(false);
	let error = $state('');

	onMount(async () => {
		try {
			rows = await api.network();
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
	});

	const groups = $derived(groupPorts(rows));
</script>

<svelte:head><title>Network · Homelab</title></svelte:head>

<div class="page">
	<a class="back" href="/settings">← Settings</a>
	<div class="page-head"><h1>Network</h1></div>
	<p class="muted intro">The ports Homelab and its apps open on this device. A port open to the home network can be reached by any device at home, and most have no Homelab sign-in. This page only shows them. It changes nothing.</p>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if !error}
		<p class="summary" role="status">{openSummary(rows)}</p>
		{#each groups as g (g.key)}
			<section class="card" aria-labelledby={`h-${g.key}`}>
				<h2 id={`h-${g.key}`}>{g.title}</h2>
				<ul>
					{#each g.rows as r, i (i)}
						<li>
							<div class="line">
								<span class="what"><strong>{serviceWords(r)}</strong> <code>{portText(r)}</code>{#if r.containerPort && r.containerPort !== r.hostPort} <span class="muted small">to {r.containerPort} inside</span>{/if}</span>
								<span class="chip" class:open={isOpen(r)}>{reachWords(r)}</span>
							</div>
							{#if r.note}<p class="muted small">{r.note}</p>{/if}
							{#if r.hint}<p class="notice hint">{r.hint}</p>{/if}
						</li>
					{/each}
				</ul>
			</section>
		{/each}
		{#if groups.length === 0}<p class="muted">Nothing is open yet.</p>{/if}
	{/if}
</div>

<style>
	.intro,
	.summary,
	.loading {
		margin: 0;
	}
	.summary {
		font-weight: 600;
	}
	h2 {
		margin: 0 0 0.5rem;
		font-size: 1rem;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
	}
	li {
		padding: 0.6rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
		padding-top: 0;
	}
	.line {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 0.4rem 0.75rem;
	}
	.what {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.chip.open {
		border-color: var(--warn-border);
		background: var(--warn-bg);
	}
	li p {
		margin: 0.3rem 0 0;
	}
	.hint {
		font-size: 0.875rem;
	}
</style>
