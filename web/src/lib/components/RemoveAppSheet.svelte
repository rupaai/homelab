<script lang="ts">
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { AppView } from '#lib/types.ts';
	import Sheet from './Sheet.svelte';

	let { app, onclose, ondone }: { app: AppView; onclose: () => void; ondone: () => void } = $props();

	let typed = $state('');
	let error = $state('');
	let sending = $state(false);
	const matches = $derived(typed.trim() === app.id);

	async function confirm(e: Event) {
		e.preventDefault();
		if (!matches) return;
		sending = true;
		error = '';
		try {
			await api.removeApp(app.id);
			ondone();
		} catch (err) {
			error = messageOf(err);
			sending = false;
		}
	}
</script>

<Sheet title="Remove {app.name}" {onclose}>
	{#snippet children()}
		<form id="remove-form" onsubmit={confirm}>
			<p>This is what happens:</p>
			<ul>
				<li>Its dev and prod stop running.</li>
				<li>Its addresses go away.</li>
				<li>Its secrets are deleted.</li>
				<li>Its data folders and data copies stay on this computer.</li>
				<li>The code mirror stays on this computer.</li>
			</ul>
			<label for="remove-confirm">Type <code>{app.id}</code> to confirm</label>
			<!-- svelte-ignore a11y_autofocus -->
			<input id="remove-confirm" type="text" bind:value={typed} autocomplete="off" autocapitalize="off" spellcheck="false" autofocus />
			{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
		</form>
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn danger" type="submit" form="remove-form" disabled={!matches || sending}>{sending ? 'Removing…' : 'Remove app'}</button>
	{/snippet}
</Sheet>

<style>
	ul {
		margin: 0.3rem 0 0;
		padding-left: 1.2rem;
	}
	.notice {
		margin-top: 0.75rem;
	}
</style>
