<script lang="ts">
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { AppView } from '#lib/types.ts';
	import Sheet from './Sheet.svelte';

	let { app, onclose, ondone }: { app: AppView; onclose: () => void; ondone: () => void } = $props();

	let error = $state('');
	let sending = $state(false);
	const build = $derived(app.latestBuild);
	const devOff = $derived(!!app.envs.find((e) => e.env === 'dev')?.off);

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

<Sheet title={devOff ? 'Get the latest build' : 'Deploy the latest build'} {onclose}>
	{#snippet children()}
		{#if devOff}
			<p>
				Dev is off, so this only saves the newest passing build{#if build} (<code>{build.short}</code>){/if} for dev. Nothing starts. Promote can now send it to prod. Prod isn't touched.
			</p>
		{:else}
		<p>
			This puts the newest passing build{#if build} (<code>{build.short}</code>){/if} on dev. It replaces what's running on dev now. Prod isn't touched.
		</p>
		{/if}
		{#if build?.url}<p class="small"><a href={build.url} target="_blank" rel="noopener">See the build<span class="sr-only"> (opens in a new tab)</span></a></p>{/if}
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={sending}>{sending ? 'Starting…' : devOff ? 'Save for dev' : 'Deploy to dev'}</button>
	{/snippet}
</Sheet>
