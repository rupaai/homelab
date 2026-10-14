<script lang="ts">
	import { absoluteTime, relativeTime } from '#lib/format.ts';
	import { clock } from '#lib/clock.svelte.ts';

	let { at }: { at: string | null | undefined } = $props();
</script>

{#if at}<time datetime={at} title={absoluteTime(at)}>{relativeTime(at, clock.now)}</time>{/if}
