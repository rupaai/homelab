<script lang="ts">
	import type { ReleaseView } from '#lib/types.ts';
	import Time from './Time.svelte';

	let { release, empty = 'Nothing deployed yet' }: { release: ReleaseView | null; empty?: string } = $props();
</script>

{#if release}
	<div class="release">
		<p class="msg">
			<a class="sha" href={release.commitUrl} target="_blank" rel="noopener">{release.short}</a>
			{release.message}
		</p>
		<p class="meta">{release.author} · <Time at={release.when} /></p>
	</div>
{:else}
	<p class="muted">{empty}</p>
{/if}

<style>
	.release p {
		margin: 0;
	}
	.msg {
		overflow-wrap: anywhere;
	}
	.sha {
		font-family: var(--mono);
		font-size: 0.9em;
		background: var(--chip);
		padding: 0.05rem 0.4rem;
		border-radius: 6px;
		margin-right: 0.3rem;
		text-decoration: none;
	}
	.meta {
		color: var(--muted);
		font-size: 0.875rem;
		margin-top: 0.1rem;
	}
</style>
