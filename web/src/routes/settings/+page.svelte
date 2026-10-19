<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { SettingView } from '#lib/types.ts';
	import SecretRow from '#lib/components/SecretRow.svelte';
	import SecretSheet from '#lib/components/SecretSheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	let items = $state<SettingView[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let editing = $state<SettingView | null>(null);

	async function load() {
		try {
			items = await api.settings();
			error = '';
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
	}
	onMount(load);

	async function save(value: string) {
		await api.setSetting(editing!.name, value);
		editing = null;
		await load();
	}
</script>

<svelte:head><title>Settings · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><h1>Settings</h1><span class="head-links"><a class="btn small" href="/settings/keys">API keys</a><a class="btn small" href="/resources">Resources</a></span></div>
	<p class="muted intro">Passwords and keys Homelab itself needs. Values can be replaced but not shown again.</p>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else}
		<section class="card" aria-label="Homelab credentials">
			<ul>
				{#each items as s (s.name)}
					<SecretRow name={s.name} set={s.set} updatedAt={s.updatedAt} updatedBy={s.updatedBy} note={s.description} extra={s.appliesAfterRestart ? 'Applies after Homelab restarts' : undefined}>
						{#snippet actions()}
							<button class="btn small" type="button" onclick={() => (editing = s)}>
								{s.set ? 'Replace' : 'Set'}<span class="sr-only"> {s.name}</span>
							</button>
						{/snippet}
					</SecretRow>
				{/each}
			</ul>
		</section>
	{/if}

	{#if editing}
		<SecretSheet
			title="{editing.set ? 'Replace' : 'Set'} {editing.name}"
			name={editing.name}
			intro={editing.appliesAfterRestart ? 'Applies after Homelab restarts.' : undefined}
			onsave={save}
			onclose={() => (editing = null)}
		/>
	{/if}
</div>

<style>
	.head-links {
		display: flex;
		gap: 0.4rem;
	}
	.intro,
	.loading {
		margin: 0;
	}
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
</style>
