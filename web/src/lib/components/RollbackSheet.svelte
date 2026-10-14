<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { envLabel } from '#lib/format.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { EnvName, RollbackPreview } from '#lib/types.ts';
	import ReleaseLine from './ReleaseLine.svelte';
	import Sheet from './Sheet.svelte';
	import Spinner from './Spinner.svelte';

	let { appId, env, onclose, ondone }: { appId: string; env: EnvName; onclose: () => void; ondone: () => void } = $props();

	let preview = $state<RollbackPreview | null>(null);
	let error = $state('');
	let sending = $state(false);

	onMount(async () => {
		try {
			preview = await api.rollbackPreview(appId, env);
		} catch (e) {
			error = messageOf(e);
		}
	});

	async function confirm() {
		sending = true;
		error = '';
		try {
			await api.rollback(appId, env);
			ondone();
		} catch (e) {
			error = messageOf(e);
			sending = false;
		}
	}
	const blocked = $derived(!preview || preview.blockers.length > 0 || !preview.to);
</script>

<Sheet title="Roll back {envLabel(env)}" {onclose}>
	{#snippet children()}
		{#if !preview && !error}
			<p class="loading"><Spinner /> Finding the earlier version…</p>
		{/if}
		{#if preview}
			<h3>{envLabel(env)} runs now</h3>
			<ReleaseLine release={preview.from} />
			<p class="arrow" aria-hidden="true">↓</p>
			<h3>{envLabel(env)} will go back to</h3>
			<ReleaseLine release={preview.to} empty="No earlier version" />
			{#if preview.note}<p class="note">{preview.note}</p>{/if}
			{#if preview.blockers.length}
				<div class="notice bad">
					<strong>Can't roll back yet</strong>
					<ul>{#each preview.blockers as b}<li>{b}</li>{/each}</ul>
				</div>
			{/if}
		{/if}
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={blocked || sending}>{sending ? 'Starting…' : `Roll back ${envLabel(env)}`}</button>
	{/snippet}
</Sheet>

<style>
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.arrow {
		margin: 0.4rem 0 0;
		color: var(--muted);
		font-size: 1.2rem;
	}
	.note {
		margin: 1rem 0 0;
	}
	.notice {
		margin-top: 1rem;
	}
</style>
