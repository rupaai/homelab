<script lang="ts">
	import { page } from '$app/state';
	import { onDestroy, onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { messageOf } from '#lib/errors.ts';
	import { envLabel } from '#lib/format.ts';
	import type { EnvName, EnvSecrets, SecretView } from '#lib/types.ts';
	import RevealedValue from '#lib/components/RevealedValue.svelte';
	import SecretHistory from '#lib/components/SecretHistory.svelte';
	import SecretRow from '#lib/components/SecretRow.svelte';
	import SecretSheet from '#lib/components/SecretSheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	const id = $derived(page.params.id ?? '');
	const app = $derived(appsStore.get(id));

	let envs = $state<EnvSecrets[]>([]);
	let canSeeValues = $state(false);
	let loaded = $state(false);
	let error = $state('');
	let editing = $state<null | { env: EnvName; secret: SecretView }>(null);
	let history = $state<null | { env: EnvName; name: string }>(null);
	let deleting = $state('');
	// Values people asked to see, by "env/NAME". Only kept while this page is open.
	let revealed = $state<Record<string, string>>({});
	let revealing = $state('');

	const keyOf = (env: EnvName, name: string) => `${env}/${name}`;

	async function show(env: EnvName, name: string) {
		const key = keyOf(env, name);
		revealing = key;
		error = '';
		try {
			revealed[key] = await api.revealSecret(id, env, name);
		} catch (e) {
			error = messageOf(e);
		} finally {
			revealing = '';
		}
	}

	function hide(env: EnvName, name: string) {
		delete revealed[keyOf(env, name)];
	}

	async function removeSaved(env: EnvName, name: string) {
		deleting = `saved/${env}/${name}`;
		error = '';
		hide(env, `saved/${name}`);
		try {
			await api.deleteSavedSecret(id, env, name);
			await load();
		} catch (e) {
			error = messageOf(e);
		} finally {
			deleting = '';
		}
	}

	async function showSaved(env: EnvName, name: string) {
		const key = keyOf(env, `saved/${name}`);
		revealing = key;
		error = '';
		try {
			revealed[key] = await api.revealSavedSecret(id, env, name);
		} catch (e) {
			error = messageOf(e);
		} finally {
			revealing = '';
		}
	}

	async function makeNew(env: EnvName, name: string) {
		error = '';
		hide(env, name);
		try {
			await api.regenerateSecret(id, env, name);
			await load();
		} catch (e) {
			error = messageOf(e);
		}
	}

	async function load() {
		try {
			const r = await api.appSecrets(id);
			envs = r.envs;
			canSeeValues = r.canSeeValues;
			error = '';
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
	}

	onMount(() => {
		if (!appsStore.loaded) appsStore.refresh();
		load();
	});

	// A value never outlives the page.
	onDestroy(() => {
		revealed = {};
	});

	async function save(value: string) {
		const t = editing!;
		await api.setSecret(id, t.env, t.secret.name, value);
		hide(t.env, t.secret.name);
		editing = null;
		await load();
		appsStore.refresh();
	}

	async function restore(version: number) {
		const h = history!;
		await api.restoreSecret(id, h.env, h.name, version);
		hide(h.env, h.name);
		history = null;
		await load();
		appsStore.refresh();
	}

	async function remove(env: EnvName, name: string) {
		deleting = `${env}/${name}`;
		error = '';
		hide(env, name);
		try {
			await api.deleteSecret(id, env, name);
			await load();
		} catch (e) {
			error = messageOf(e);
		} finally {
			deleting = '';
		}
	}
</script>

<svelte:head><title>Secrets · {app?.name ?? id} · Homelab</title></svelte:head>

<div class="page">
	<a class="back" href="/apps/{encodeURIComponent(id)}">← {app?.name ?? 'Back to the app'}</a>
	<h1>Secrets{app ? ` for ${app.name}` : ''}</h1>
	<p class="muted intro">
		Dev and prod are kept separate on purpose.
		{#if canSeeValues}
			Showing a value is written to Activity.
		{:else}
			You can see which secrets are set. Only the owner, or people the owner allows, can see or change values.
		{/if}
	</p>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else}
		{#each envs as group (group.env)}
			<section class="card" aria-labelledby="sec-{group.env}">
				<h2 id="sec-{group.env}">{envLabel(group.env)}</h2>
				{#if group.secrets.length}
					<ul>
						{#each group.secrets as s (s.name)}
							<SecretRow
								name={s.name}
								set={s.set}
								updatedAt={s.updatedAt}
								updatedBy={s.updatedBy}
								note={s.declared ? undefined : "This app doesn't list it any more."}
								extra={s.generated ? (s.set ? 'Made by Homelab' : 'Made on next deploy') : undefined}
							>
								{#snippet actions()}
									{#if canSeeValues}
										{@const key = keyOf(group.env, s.name)}
										{#if s.set}
											<button class="btn small" type="button" disabled={revealing === key} onclick={() => (key in revealed ? hide(group.env, s.name) : show(group.env, s.name))}>
												{key in revealed ? 'Hide' : revealing === key ? 'Showing…' : 'Show'}<span class="sr-only"> {s.name} for {envLabel(group.env)}</span>
											</button>
											<button class="btn small" type="button" onclick={() => (history = { env: group.env, name: s.name })}>
												History<span class="sr-only"> of {s.name} for {envLabel(group.env)}</span>
											</button>
										{/if}
										{#if s.generated}
											{#if s.set}
												<button class="btn small" type="button" onclick={() => makeNew(group.env, s.name)}>
													Make new<span class="sr-only"> {s.name} for {envLabel(group.env)}</span>
												</button>
											{/if}
										{:else if s.declared}
											<button class="btn small" type="button" onclick={() => (editing = { env: group.env, secret: s })}>
												{s.set ? 'Replace' : 'Set'}<span class="sr-only"> {s.name} for {envLabel(group.env)}</span>
											</button>
										{:else}
											<button class="btn small" type="button" disabled={deleting === key} onclick={() => remove(group.env, s.name)}>
												Delete<span class="sr-only"> {s.name} from {envLabel(group.env)}</span>
											</button>
										{/if}
									{/if}
								{/snippet}
								{#snippet below()}
									{#if keyOf(group.env, s.name) in revealed}
										<RevealedValue
											value={revealed[keyOf(group.env, s.name)]}
											label="{s.name} for {envLabel(group.env)}"
											onhide={() => hide(group.env, s.name)}
										/>
									{/if}
								{/snippet}
							</SecretRow>
						{/each}
					</ul>
				{:else}
					<p class="muted">This app doesn't need any secrets for {group.env}.</p>
				{/if}
				{#if group.saved.length}
					<h3>Saved by the app</h3>
					<p class="muted small">The app made these while it runs. They are not used when it starts.</p>
					<ul>
						{#each group.saved as s (s.name)}
							{@const key = keyOf(group.env, `saved/${s.name}`)}
							<SecretRow name={s.name} set={true} version={s.version} updatedAt={s.updatedAt}>
								{#snippet actions()}
									{#if canSeeValues}
										<button class="btn small" type="button" disabled={revealing === key} onclick={() => (key in revealed ? hide(group.env, `saved/${s.name}`) : showSaved(group.env, s.name))}>
											{key in revealed ? 'Hide' : revealing === key ? 'Showing…' : 'Show'}<span class="sr-only"> {s.name} saved by the app for {envLabel(group.env)}</span>
										</button>
										<button class="btn small" type="button" disabled={deleting === `saved/${group.env}/${s.name}`} onclick={() => removeSaved(group.env, s.name)}>
											Delete<span class="sr-only"> {s.name} saved by the app for {envLabel(group.env)}</span>
										</button>
									{/if}
								{/snippet}
								{#snippet below()}
									{#if key in revealed}
										<RevealedValue value={revealed[key]} label="{s.name} saved by the app for {envLabel(group.env)}" onhide={() => hide(group.env, `saved/${s.name}`)} />
									{/if}
								{/snippet}
							</SecretRow>
						{/each}
					</ul>
				{/if}
			</section>
		{/each}
	{/if}

	{#if editing}
		<SecretSheet
			title="{editing.secret.set ? 'Replace' : 'Set'} {editing.secret.name} for {envLabel(editing.env)}"
			name={editing.secret.name}
			intro={editing.secret.set ? `This replaces the value ${envLabel(editing.env)} uses now. The old one stays in History.` : undefined}
			onsave={save}
			onclose={() => (editing = null)}
		/>
	{/if}

	{#if history}
		<SecretHistory
			title="History of {history.name} for {envLabel(history.env)}"
			name={history.name}
			load={() => api.secretVersions(id, history!.env, history!.name)}
			onrestore={restore}
			onclose={() => (history = null)}
		/>
	{/if}
</div>

<style>
	.back {
		text-decoration: none;
		font-weight: 600;
		align-self: flex-start;
		padding: 0.25rem 0;
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
	h2 {
		margin: 0 0 0.3rem;
	}
	h3 {
		margin: 1rem 0 0.1rem;
		font-size: 1rem;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
</style>
