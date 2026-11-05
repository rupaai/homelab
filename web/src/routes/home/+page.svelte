<script lang="ts">
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';

	// The apps list now lives at `/`. This keeps old links working.
	onMount(() => { void goto('/', { replaceState: true }); });
</script>

<svelte:head><title>Apps · Homelab</title></svelte:head>

<div class="page"><p class="muted">Opening your apps…</p></div>
