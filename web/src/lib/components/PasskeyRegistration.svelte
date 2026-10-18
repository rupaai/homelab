<script lang="ts">
	import { onMount } from 'svelte';

	let { url, continueLabel, onContinue, onRefresh, refreshDisabled = false }: { url: string; continueLabel: string; onContinue: () => void; onRefresh?: () => void; refreshDisabled?: boolean } = $props();
	let opened = false;
	let wasHidden = false;
	let canContinue = $state(false);

	onMount(() => {
		function trackReturn() {
			if (document.visibilityState === 'hidden') {
				if (opened) wasHidden = true;
				return;
			}
			if (wasHidden) canContinue = true;
		}
		document.addEventListener('visibilitychange', trackReturn);
		return () => document.removeEventListener('visibilitychange', trackReturn);
	});
</script>

<p>Pocket ID will open its passkey setup page. On the Pocket ID page, click Add passkey.</p>
<a class="btn primary" href={url} target="_blank" rel="noopener" onclick={() => opened = true}>Register passkey</a>
{#if canContinue}
	<p>After adding your passkey, return here to continue.</p>
	{#if onRefresh}<button class="btn" type="button" onclick={onRefresh} disabled={refreshDisabled}>{refreshDisabled ? 'Preparing…' : 'Get a new setup link'}</button>{/if}
	<button class="btn" type="button" onclick={onContinue}>{continueLabel}</button>
{/if}
