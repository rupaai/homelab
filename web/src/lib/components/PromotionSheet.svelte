<script lang="ts">
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { promotionHelp, promotionLabel } from '#lib/format.ts';
	import type { AppView, PromotionMode } from '#lib/types.ts';
	import Sheet from './Sheet.svelte';

	let { app, mode, onclose, ondone }: { app: AppView; mode: PromotionMode; onclose: () => void; ondone: () => void } = $props();

	let error = $state('');
	let sending = $state(false);

	const happens = $derived(
		mode === 'manual'
			? `Homelab will stop deploying and promoting ${app.name} by itself. Builds wait until you deploy them.`
			: mode === 'auto-dev'
				? `New builds that pass will go to dev by themselves. Nothing goes to prod until you promote it.`
				: `New builds will go to dev by themselves. Then, once dev has stayed healthy for ${app.promoteAfter || 'a while'} and its smoke test passes, Homelab promotes them to prod without asking. If prod has trouble in the 10 minutes after, Homelab puts the earlier version back.`
	);

	async function confirm() {
		sending = true;
		error = '';
		try {
			await api.setPromotion(app.id, mode);
			ondone();
		} catch (e) {
			error = messageOf(e);
			sending = false;
		}
	}
</script>

<Sheet title="Change to {promotionLabel(mode)}" {onclose}>
	{#snippet children()}
		<p>{happens}</p>
		<p class="muted small">{promotionHelp(mode, app.promoteAfter)}</p>
		{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="button" onclick={confirm} disabled={sending}>{sending ? 'Saving…' : `Use ${promotionLabel(mode)}`}</button>
	{/snippet}
</Sheet>
