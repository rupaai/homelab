<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';

	// <img src> can't send the session token, so the picture is fetched into a Blob first.
	let { folder, id, name, large = false }: { folder: string; id: string; name: string; large?: boolean } = $props();

	let src = $state('');
	let failed = $state(false);

	onMount(() => {
		let url = '';
		let gone = false;
		api.fileBlob(folder, id).then(
			(b) => {
				if (gone) return;
				url = URL.createObjectURL(b);
				src = url;
			},
			() => { if (!gone) failed = true; }
		);
		return () => {
			gone = true;
			if (url) URL.revokeObjectURL(url);
		};
	});
</script>

{#if src}
	<img class:large {src} alt={name} />
{:else}
	<span class="blank" class:large aria-hidden="true">{failed ? '!' : ''}</span>
{/if}

<style>
	img,
	.blank {
		display: block;
		width: 48px;
		height: 48px;
		object-fit: cover;
		border-radius: 8px;
		background: var(--accent-soft);
		color: var(--muted);
		text-align: center;
		line-height: 48px;
	}
	img.large,
	.blank.large {
		width: 100%;
		height: auto;
		max-height: 60vh;
		min-height: 8rem;
		object-fit: contain;
	}
</style>
