<script lang="ts">
	import { actorLabel, envLabel, isProblemAction } from '#lib/format.ts';
	import type { ActivityEvent } from '#lib/types.ts';
	import Time from './Time.svelte';

	let {
		events,
		appName,
		showApp = true
	}: { events: ActivityEvent[]; appName?: (id: string) => string; showApp?: boolean } = $props();
</script>

<ul class="events">
	{#each events as ev (ev.id)}
		<li class:problem={isProblemAction(ev.action)}>
			<p class="detail">{ev.detail}</p>
			<p class="meta">
				<span>{actorLabel(ev.actor)}</span>
				<span aria-hidden="true">·</span>
				<Time at={ev.at} />
				{#if showApp && ev.app}<span class="chip">{appName?.(ev.app) ?? ev.app}</span>{/if}
				{#if ev.env}<span class="chip">{envLabel(ev.env)}</span>{/if}
				{#if isProblemAction(ev.action)}<span class="chip bad">Problem</span>{/if}
			</p>
		</li>
	{/each}
</ul>

<style>
	.events {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		padding: 0.7rem 0;
		border-bottom: 1px solid var(--border);
		border-left: 3px solid transparent;
		padding-left: 0.7rem;
	}
	li:last-child {
		border-bottom: 0;
	}
	li.problem {
		border-left-color: var(--bad);
	}
	p {
		margin: 0;
	}
	.detail {
		overflow-wrap: anywhere;
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem;
		color: var(--muted);
		font-size: 0.85rem;
		margin-top: 0.2rem;
	}
	.chip.bad {
		background: var(--bad-bg);
		color: var(--bad);
	}
</style>
