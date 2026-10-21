<script lang="ts">
	import Time from './Time.svelte';
	import type { Snippet } from 'svelte';

	// One row in a list of secrets: the name, whether it has a value, who changed it and when.
	let {
		name,
		set,
		updatedAt,
		updatedBy,
		note,
		extra,
		version,
		actions,
		below
	}: { name: string; set: boolean; updatedAt?: string; updatedBy?: string; note?: string; extra?: string; version?: number; actions?: Snippet; below?: Snippet } = $props();
</script>

<li>
	<div class="info">
		<code>{name}</code>
		{#if version === undefined}<span class="chip" class:missing={!set}>{set ? 'Set' : 'Missing'}</span>{/if}
		{#if note}<p class="muted small">{note}</p>{/if}
		{#if extra}<p class="small extra">{extra}</p>{/if}
		{#if set && updatedAt}
			<p class="muted small">Updated <Time at={updatedAt} />{updatedBy ? ` by ${updatedBy === 'homelab' ? 'Homelab' : updatedBy}` : ''}{version !== undefined ? ` · version ${version}` : ''}</p>
		{/if}
	</div>
	{#if actions}<div class="actions">{@render actions()}</div>{/if}
	{#if below}<div class="below">{@render below()}</div>{/if}
</li>

<style>
	li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.75rem;
		padding: 0.7rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	.info {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.info p {
		margin: 0.15rem 0 0;
	}
	.chip.missing {
		background: var(--warn-bg);
		color: var(--warn);
		border: 1px solid var(--warn-border);
	}
	.extra {
		color: var(--warn);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: 0.4rem;
	}
	.below {
		flex-basis: 100%;
	}
</style>
