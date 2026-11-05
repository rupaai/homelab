<script lang="ts">
	import { api } from '#lib/api.ts';
	import { envLabel } from '#lib/format.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { EnvName } from '#lib/types.ts';
	import Sheet from './Sheet.svelte';

	let { appId, env, onclose, ondone }: { appId: string; env: EnvName; onclose: () => void; ondone: () => void } = $props();

	let error = $state('');
	let sending = $state(false);

	async function confirm() {
		sending = true;
		error = '';
		try {
			await api.restart(appId, env);
			ondone();
		} catch (e) {
			error = messageOf(e);
			sending = false;
		}
	}
</script>

<Sheet title="Restart {envLabel(env)}" {onclose}>
	{#snippet children()}
		<p>
			This makes fresh containers for {env} from the version it runs now. The app is away for a few seconds. Use it when the app is stuck or after you change a secret.
		</p>
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={sending}>{sending ? 'Restarting…' : `Restart ${env}`}</button>
	{/snippet}
</Sheet>
