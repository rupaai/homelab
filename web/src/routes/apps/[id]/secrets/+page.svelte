<script lang="ts">
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { messageOf } from '#lib/errors.ts';
	import { envLabel } from '#lib/format.ts';
	import type { EnvName, EnvSecrets, SecretView } from '#lib/types.ts';
	import SecretRow from '#lib/components/SecretRow.svelte';
	import SecretSheet from '#lib/components/SecretSheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	const id = $derived(page.params.id ?? '');
	const app = $derived(appsStore.get(id));

	let envs = $state<EnvSecrets[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let editing = $state<null | { env: EnvName; secret: SecretView }>(null);
	let deleting = $state('');
	let copied = $state('');

	async function copy(key: string, value: string) {
		try {
			await navigator.clipboard.writeText(value);
			copied = key;
			setTimeout(() => { if (copied === key) copied = ''; }, 2000);
		} catch {
			error = 'Your browser could not copy it. Select and copy it instead.';
		}
	}

	async function makeNew(env: EnvName, name: string) {
		error = '';
		try {
			await api.regenerateSecret(id, env, name);
			await load();
		} catch (e) {
			error = messageOf(e);
		}
	}

	async function load() {
		try {
			envs = await api.appSecrets(id);
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

	async function save(value: string) {
		const t = editing!;
		await api.setSecret(id, t.env, t.secret.name, value);
		editing = null;
		await load();
		appsStore.refresh();
	}

	async function remove(env: EnvName, name: string) {
		deleting = `${env}/${name}`;
		error = '';
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
	<p class="muted intro">Dev and prod are kept separate on purpose. Values can be replaced but not shown again.</p>

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
									{#if s.generated}
										{#if s.value}
											<code class="value">{s.value}</code>
											<button class="btn small" type="button" onclick={() => copy(`${group.env}/${s.name}`, s.value ?? '')}>
												{copied === `${group.env}/${s.name}` ? 'Copied' : 'Copy'}<span class="sr-only"> {s.name} for {envLabel(group.env)}</span>
											</button>
											<button class="btn small" type="button" onclick={() => makeNew(group.env, s.name)}>
												Make new<span class="sr-only"> {s.name} for {envLabel(group.env)}</span>
											</button>
										{/if}
									{:else if s.declared}
										<button class="btn small" type="button" onclick={() => (editing = { env: group.env, secret: s })}>
											{s.set ? 'Replace' : 'Set'}<span class="sr-only"> {s.name} for {envLabel(group.env)}</span>
										</button>
									{:else}
										<button class="btn small" type="button" disabled={deleting === `${group.env}/${s.name}`} onclick={() => remove(group.env, s.name)}>
											Delete<span class="sr-only"> {s.name} from {envLabel(group.env)}</span>
										</button>
									{/if}
								{/snippet}
							</SecretRow>
						{/each}
					</ul>
				{:else}
					<p class="muted">This app doesn't need any secrets for {group.env}.</p>
				{/if}
			</section>
		{/each}
	{/if}

	{#if editing}
		<SecretSheet
			title="{editing.secret.set ? 'Replace' : 'Set'} {editing.secret.name} for {envLabel(editing.env)}"
			name={editing.secret.name}
			intro={editing.secret.set ? `This replaces the value ${envLabel(editing.env)} uses now. The old one can't be shown again.` : undefined}
			onsave={save}
			onclose={() => (editing = null)}
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
	.value {
		align-self: center;
		overflow-wrap: anywhere;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
</style>
