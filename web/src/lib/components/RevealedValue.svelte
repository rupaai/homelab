<script lang="ts">
	import { onDestroy } from 'svelte';
	import { copyText } from '#lib/clipboard.ts';

	// A secret's value, shown after someone asked for it. It lives only in this component, so it
	// is gone when the person hides it or leaves the page.
	let { value, label, onhide }: { value: string; label: string; onhide: () => void } = $props();

	let copied = $state(false);
	let failed = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		failed = false;
		if (await copyText(value)) {
			copied = true;
			clearTimeout(timer);
			timer = setTimeout(() => (copied = false), 2000);
		} else {
			failed = true;
		}
	}

	onDestroy(() => clearTimeout(timer));
</script>

<div class="revealed">
	<code class="value" aria-label="Value of {label}">{value}</code>
	<button class="btn small" type="button" onclick={copy}>{copied ? 'Copied' : 'Copy'}<span class="sr-only"> {label}</span></button>
	<button class="btn small" type="button" onclick={onhide}>Hide<span class="sr-only"> {label}</span></button>
	{#if failed}<p class="small bad-text" role="alert">Your browser could not copy it. Select and copy it instead.</p>{/if}
</div>

<style>
	.revealed {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}
	.value {
		flex: 1 1 14rem;
		min-width: 0;
		overflow-wrap: anywhere;
		user-select: all;
	}
	.bad-text {
		flex-basis: 100%;
		margin: 0;
		color: var(--bad, inherit);
	}
</style>
