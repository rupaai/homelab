<script lang="ts">
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { AppView } from '#lib/types.ts';
	import Sheet from './Sheet.svelte';

	let { app, onclose, ondone }: { app: AppView; onclose: () => void; ondone: () => void } = $props();

	let error = $state('');
	let sending = $state(false);
	const build = $derived(app.latestBuild);

	async function confirm() {
		sending = true;
		error = '';
		try {
			await api.deploy(app.id);
			ondone();
		} catch (e) {
			error = messageOf(e);
			sending = false;
		}
	}
</script>

<Sheet title="Deploy the latest build" {onclose}>
	{#snippet children()}
		<p>
			This puts the newest passing build{#if build} (<code>{build.short}</code>){/if} on dev. It replaces what's running on dev now. Prod isn't touched.
		</p>
		{#if build?.url}<p class="small"><a href={build.url} target="_blank" rel="noopener">See the build<span class="sr-only"> (opens in a new tab)</span></a></p>{/if}
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={sending}>{sending ? 'Starting…' : 'Deploy to dev'}</button>
	{/snippet}
</Sheet>
