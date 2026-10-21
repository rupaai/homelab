<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { SettingView } from '#lib/types.ts';
	import RevealedValue from '#lib/components/RevealedValue.svelte';
	import SecretHistory from '#lib/components/SecretHistory.svelte';
	import SecretRow from '#lib/components/SecretRow.svelte';
	import SecretSheet from '#lib/components/SecretSheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	let items = $state<SettingView[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let editing = $state<SettingView | null>(null);
	let history = $state<SettingView | null>(null);
	let canSeeValues = $state(false);
	// Values people asked to see, by name. Only kept while this page is open.
	let revealed = $state<Record<string, string>>({});
	let revealing = $state('');

	async function show(name: string) {
		revealing = name;
		error = '';
		try {
			revealed[name] = await api.revealSetting(name);
		} catch (e) {
			error = messageOf(e);
		} finally {
			revealing = '';
		}
	}

	async function restore(version: number) {
		const h = history!;
		await api.restoreSetting(h.name, version);
		delete revealed[h.name];
		history = null;
		await load();
	}

	onDestroy(() => {
		revealed = {};
	});

	async function load() {
		try {
			[items, canSeeValues] = await Promise.all([api.settings(), api.me().then((m) => m.canSeeSecrets === true)]);
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
		delete revealed[editing!.name];
		editing = null;
		await load();
	}
</script>

<svelte:head><title>Settings · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><h1>Settings</h1><span class="head-links"><a class="btn small" href="/settings/keys">API keys</a><a class="btn small" href="/resources">Resources</a></span></div>
	<p class="muted intro">
		Passwords and keys Homelab itself needs.
		{#if canSeeValues}
			Showing a value is written to Activity.
		{:else}
			Only the owner, or people the owner allows, can see or change values.
		{/if}
	</p>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else}
		<section class="card" aria-label="Homelab credentials">
			<ul>
				{#each items as s (s.name)}
					<SecretRow name={s.name} set={s.set} updatedAt={s.updatedAt} updatedBy={s.updatedBy} note={s.description} extra={s.appliesAfterRestart ? 'Applies after Homelab restarts' : undefined}>
						{#snippet actions()}
							{#if canSeeValues}
								{#if s.set}
									<button class="btn small" type="button" disabled={revealing === s.name} onclick={() => (s.name in revealed ? delete revealed[s.name] : show(s.name))}>
										{s.name in revealed ? 'Hide' : revealing === s.name ? 'Showing…' : 'Show'}<span class="sr-only"> {s.name}</span>
									</button>
									<button class="btn small" type="button" onclick={() => (history = s)}>
										History<span class="sr-only"> of {s.name}</span>
									</button>
								{/if}
								<button class="btn small" type="button" onclick={() => (editing = s)}>
									{s.set ? 'Replace' : 'Set'}<span class="sr-only"> {s.name}</span>
								</button>
							{/if}
						{/snippet}
						{#snippet below()}
							{#if s.name in revealed}
								<RevealedValue value={revealed[s.name]} label={s.name} onhide={() => delete revealed[s.name]} />
							{/if}
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

	{#if history}
		<SecretHistory
			title="History of {history.name}"
			name={history.name}
			load={() => api.settingVersions(history!.name)}
			onrestore={restore}
			onclose={() => (history = null)}
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
