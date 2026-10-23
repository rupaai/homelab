<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { noteLines, promoteTitle } from '#lib/format.ts';
	import type { PromotePreview } from '#lib/types.ts';
	import ReleaseLine from './ReleaseLine.svelte';
	import Sheet from './Sheet.svelte';
	import Spinner from './Spinner.svelte';

	let { appId, onclose, ondone }: { appId: string; onclose: () => void; ondone: () => void } = $props();

	let preview = $state<PromotePreview | null>(null);
	let error = $state('');
	let sending = $state(false);

	onMount(async () => {
		try {
			preview = await api.promotePreview(appId);
		} catch (e) {
			error = messageOf(e);
		}
	});

	async function confirm() {
		sending = true;
		error = '';
		try {
			await api.promote(appId);
			ondone();
		} catch (e) {
			error = messageOf(e);
			sending = false;
		}
	}
	const blocked = $derived(!preview || preview.blockers.length > 0);
	const notes = $derived(noteLines(preview?.notes));
</script>

<Sheet title="Promote to prod" {onclose}>
	{#snippet children()}
		{#if !preview && !error}
			<p class="loading"><Spinner /> Checking what would change…</p>
		{/if}
		{#if preview}
			<p class="lead">
				{#if preview.nextVersion}<strong>{promoteTitle(preview.to?.version, preview.nextVersion)}.</strong>{/if}
				This puts the exact build running on dev onto prod.
			</p>
			{#if preview.summary}<p class="summary">{preview.summary}</p>{/if}
			{#if notes.length}
				<ul class="notes" aria-label="Release notes">
					{#each notes as line}<li>{line}</li>{/each}
				</ul>
			{/if}

			<h3>Prod runs now</h3>
			<ReleaseLine release={preview.from} empty="Nothing yet. This is prod's first version." />
			<p class="arrow" aria-hidden="true">↓</p>
			<h3>Prod will run</h3>
			<ReleaseLine release={preview.to} />

			{#if preview.commits.length}
				<h3>What's new ({preview.commits.length} {preview.commits.length === 1 ? 'change' : 'changes'})</h3>
				<ul class="commits">
					{#each preview.commits as c (c.sha)}
						<li><a href={c.url} target="_blank" rel="noopener">{c.message}</a><span class="muted small by">· {c.author}</span></li>
					{/each}
				</ul>
			{/if}
			{#if preview.compareUrl}
				<p class="small"><a href={preview.compareUrl} target="_blank" rel="noopener">Compare the versions on GitHub<span class="sr-only"> (opens in a new tab)</span></a></p>
			{/if}

			{#if preview.dataCopy}
				<p class="notice info"><strong>Prod's data will be copied first</strong>That's your safety copy. Prod stops for a moment while it happens.</p>
			{/if}

			{#if preview.blockers.length}
				<div class="notice bad">
					<strong>Can't promote yet</strong>
					<ul>{#each preview.blockers as b}<li>{b}</li>{/each}</ul>
				</div>
			{/if}
			{#if preview.missingSecrets.length}
				<p class="small">Add values for these secrets on prod: {#each preview.missingSecrets as s, i}<code>{s}</code>{i < preview.missingSecrets.length - 1 ? ', ' : ''}{/each}</p>
			{/if}
		{/if}
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={blocked || sending}>{sending ? 'Starting…' : 'Promote to prod'}</button>
	{/snippet}
</Sheet>

<style>
	.lead {
		margin: 0.2rem 0 0;
	}
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
	.summary {
		margin: 0.3rem 0 0;
		color: var(--muted);
	}
	.notes {
		margin: 0.4rem 0 0;
		padding-left: 1.1rem;
	}
	.commits {
		margin: 0;
		padding-left: 1.1rem;
	}
	.by {
		margin-left: 0.3rem;
	}
	.commits li {
		margin-bottom: 0.25rem;
	}
	.notice {
		margin-top: 1rem;
	}
	.notice.info {
		border-color: var(--border-strong);
		background: var(--surface-2);
	}
</style>
