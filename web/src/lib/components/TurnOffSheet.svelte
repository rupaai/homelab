<script lang="ts">
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import Sheet from './Sheet.svelte';

	let { appId, onclose, ondone }: { appId: string; onclose: () => void; ondone: () => void } = $props();

	let error = $state('');
	let sending = $state(false);

	async function confirm() {
		sending = true;
		error = '';
		try {
			await api.setOff(appId, 'dev', true);
			ondone();
		} catch (e) {
			error = messageOf(e);
			sending = false;
		}
	}
</script>

<Sheet title="Turn off dev" {onclose}>
	{#snippet children()}
		<p>Dev stops and stays stopped. Its data is kept. Prod is not touched. New builds wait until you turn it back on.</p>
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={sending}>{sending ? 'Turning off…' : 'Turn off dev'}</button>
	{/snippet}
</Sheet>
