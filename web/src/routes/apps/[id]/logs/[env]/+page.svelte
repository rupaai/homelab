<script lang="ts">
	import { page } from '$app/state';
	import { onMount, tick } from 'svelte';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { messageOf } from '#lib/errors.ts';
	import { envLabel } from '#lib/format.ts';
	import { startPolling } from '#lib/poll.ts';
	import type { EnvName } from '#lib/types.ts';

	const id = $derived(page.params.id ?? '');
	const env = $derived((page.params.env === 'prod' ? 'prod' : 'dev') as EnvName);
	const name = $derived(appsStore.get(id)?.name ?? id);

	let text = $state('');
	let loaded = $state(false);
	let error = $state('');
	let lines = $state(200);
	let auto = $state(true);
	let box: HTMLElement | undefined = $state();
	let stick = true; // follow the newest line unless the person scrolled up

	async function refresh() {
		try {
			text = await api.logs(id, env, lines);
			error = '';
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
		await tick();
		if (stick && box) box.scrollTop = box.scrollHeight;
	}

	function onscroll() {
		if (box) stick = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
	}

	// Poll while auto-refresh is on; changing the toggle or line count restarts it.
	$effect(() => {
		lines; // re-run when the line count changes
		if (!auto) {
			refresh();
			return;
		}
		return startPolling(refresh, () => 5000);
	});

	onMount(() => {
		if (!appsStore.loaded) appsStore.refresh();
	});
</script>

<svelte:head><title>{envLabel(env)} logs · {name} · Homehub</title></svelte:head>

<div class="page fill">
	<a class="back" href="/apps/{encodeURIComponent(id)}">← {name}</a>
	<h1>{envLabel(env)} logs</h1>

	<div class="controls">
		<button class="btn small" type="button" onclick={refresh}>Refresh</button>
		<label class="inline"><input type="checkbox" bind:checked={auto} /> Every 5s</label>
		<label class="inline" for="lines">Lines</label>
		<select id="lines" bind:value={lines}>
			<option value={200}>200</option>
			<option value={500}>500</option>
			<option value={1000}>1000</option>
		</select>
	</div>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<pre class="log" bind:this={box} {onscroll} tabindex="0" aria-label="{envLabel(env)} log, newest line last">{#if !loaded}Loading…{:else if text.trim() === ''}Nothing in the log yet.{:else}{text}{/if}</pre>
</div>

<style>
	.fill {
		height: 100%;
		min-height: 24rem;
	}
	.back {
		text-decoration: none;
		font-weight: 600;
		align-self: flex-start;
	}
	.controls {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.6rem 0.9rem;
	}
	label.inline {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		margin: 0;
		font-weight: 500;
	}
	input[type='checkbox'] {
		width: 1.2rem;
		height: 1.2rem;
	}
	select {
		width: auto;
		min-height: 36px;
	}
	.log {
		flex: 1;
		min-height: 0;
		margin: 0;
		overflow: auto;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 10px;
		padding: 0.7rem;
		font-size: 0.75rem;
		line-height: 1.5;
		white-space: pre;
	}
</style>
