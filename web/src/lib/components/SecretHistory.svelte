<script lang="ts">
	import { onMount } from 'svelte';
	import { messageOf } from '#lib/errors.ts';
	import type { SecretVersion } from '#lib/types.ts';
	import Sheet from './Sheet.svelte';
	import Spinner from './Spinner.svelte';
	import Time from './Time.svelte';

	// The last few values of one secret: who set each and when. Values are never listed here.
	let {
		title,
		name,
		load,
		onrestore,
		onclose
	}: {
		title: string;
		name: string;
		load: () => Promise<SecretVersion[]>;
		onrestore: (version: number) => Promise<void>;
		onclose: () => void;
	} = $props();

	let versions = $state<SecretVersion[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let restoring = $state(0);

	onMount(async () => {
		try {
			versions = await load();
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
	});

	async function restore(version: number) {
		restoring = version;
		error = '';
		try {
			await onrestore(version);
		} catch (e) {
			error = messageOf(e);
			restoring = 0;
		}
	}
</script>

<Sheet {title} {onclose}>
	{#snippet children()}
		{#if !loaded}
			<p class="muted loading"><Spinner /> Loading…</p>
		{:else if versions.length === 0}
			<p>There are no earlier values of <code>{name}</code>.</p>
		{:else}
			<p>Homelab keeps the last 5 values of <code>{name}</code>. Putting one back makes it the newest version. It is used from the next deploy.</p>
			<ul>
				{#each versions as v, i (v.version)}
					<li>
						<div>
							<strong>Version {v.version}</strong>{#if i === 0} <span class="chip">Current</span>{/if}
							<p class="muted small">Set <Time at={v.setAt} /> by {v.setBy === 'homelab' ? 'Homelab' : v.setBy}</p>
						</div>
						{#if i > 0}
							<button class="btn small" type="button" disabled={restoring !== 0} onclick={() => restore(v.version)}>
								{restoring === v.version ? 'Restoring…' : 'Restore'}<span class="sr-only"> version {v.version} of {name}</span>
							</button>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Close</button>
	{/snippet}
</Sheet>

<style>
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		padding: 0.6rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	li p {
		margin: 0.15rem 0 0;
	}
	.notice {
		margin-top: 0.75rem;
	}
</style>
