<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { buildScopes, endOfDay, keyOwner, keyProblem, scopeWords, type Grant, type GrantKind } from '#lib/keys.ts';
	import type { AppView, KeyView } from '#lib/types.ts';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';
	import Time from '#lib/components/Time.svelte';

	let adminKeys = $state<KeyView[]>([]);
	let appKeys = $state<KeyView[]>([]);
	let apps = $state<AppView[]>([]);
	let loaded = $state(false);
	let error = $state('');

	async function load() {
		try {
			const [k, a] = await Promise.all([api.keys(), api.apps()]);
			adminKeys = k.admin.filter((x) => !x.revoked);
			appKeys = k.app;
			apps = a;
			error = '';
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
		}
	}
	onMount(load);

	// New key form
	let name = $state('');
	let users = $state(false);
	let grants = $state<Grant[]>([]);
	let endDate = $state('');
	let formError = $state('');
	let sending = $state(false);
	// The new key, shown once. It lives only in this variable.
	let created = $state<string | null>(null);
	let copied = $state(false);

	const addGrant = (kind: GrantKind) => (grants = [...grants, { kind, target: kind === 'files' ? '' : (apps[0]?.id ?? ''), access: 'read' }]);
	const dropGrant = (i: number) => (grants = grants.filter((_, j) => j !== i));

	async function create(e: Event) {
		e.preventDefault();
		formError = keyProblem(name, users, grants);
		if (formError) return;
		sending = true;
		try {
			const r = await api.createKey({ name: name.trim(), scopes: buildScopes(users, grants), expires: endOfDay(endDate) });
			created = r.key;
			name = '';
			users = false;
			grants = [];
			endDate = '';
			await load();
		} catch (err) {
			formError = messageOf(err);
		} finally {
			sending = false;
		}
	}

	async function copy() {
		try {
			await navigator.clipboard.writeText(created ?? '');
			copied = true;
		} catch {
			copied = false;
			formError = 'Copying did not work. Select the key and copy it by hand.';
		}
	}
	function done() {
		created = null;
		copied = false;
		formError = '';
	}

	// Revoke
	let revoking = $state<KeyView | null>(null);
	let revokeError = $state('');
	let revokeBusy = $state(false);
	async function revoke() {
		if (!revoking) return;
		revokeBusy = true;
		revokeError = '';
		try {
			await api.revokeKey(revoking.id);
			revoking = null;
			await load();
		} catch (err) {
			revokeError = messageOf(err);
		} finally {
			revokeBusy = false;
		}
	}
	const closeRevoke = () => ((revoking = null), (revokeError = ''));
</script>

<svelte:head><title>API keys · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><h1>API keys</h1><a class="btn small" href="/settings">Settings</a></div>
	<p class="muted intro">Keys let a server outside Homelab use it, for example a photo service saving files. They work on the companion's address.</p>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else}
		<section class="card" aria-labelledby="keys-h">
			<h2 id="keys-h">Your keys</h2>
			{#if adminKeys.length === 0}
				<p class="muted none">No keys yet. Make one below.</p>
			{:else}
				<ul>
					{#each adminKeys as k (k.id)}
						<li>
							<div class="info">
								<strong>{k.name}</strong> <code>{k.prefix}…</code>
								<p class="can">{scopeWords(k.scopes).join(' · ') || 'Nothing'}</p>
								<p class="muted small">
									Made by {keyOwner(k)} · {#if k.lastUsed}Last used <Time at={k.lastUsed} />{:else}Never used{/if}{#if k.expires} · Ends <Time at={k.expires} />{/if}
								</p>
							</div>
							<button class="btn danger small" type="button" onclick={() => (revoking = k)}>Revoke<span class="sr-only"> {k.name}</span></button>
						</li>
					{/each}
				</ul>
			{/if}
		</section>

		<section class="card" aria-labelledby="new-h">
			<h2 id="new-h">New key</h2>
			{#if created}
				<div class="notice" role="status">
					<strong>Copy it now. Homelab won't show it again.</strong>
					<code class="secret">{created}</code>
				</div>
				<div class="row-actions">
					<button class="btn primary" type="button" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
					<button class="btn" type="button" onclick={done}>Done</button>
				</div>
				{#if formError}<p class="notice bad" role="alert">{formError}</p>{/if}
			{:else}
				<form onsubmit={create}>
					<label for="key-name">Name</label>
					<input id="key-name" type="text" bind:value={name} maxlength="60" autocomplete="off" placeholder="Photo service" />

					<fieldset>
						<legend>What can it do?</legend>
						<label class="check"><input type="checkbox" bind:checked={users} />Read people</label>
						{#each grants as g, i (i)}
							<div class="grant">
								<span class="kind">{g.kind === 'files' ? 'Files' : g.kind === 'db' ? 'Database' : 'Data'}</span>
								{#if g.kind === 'files'}
									<input type="text" bind:value={g.target} aria-label="Folder name" placeholder="folder name" autocapitalize="off" spellcheck="false" />
								{:else}
									<select bind:value={g.target} aria-label="App">
										{#each apps as a (a.id)}<option value={a.id}>{a.name}</option>{/each}
									</select>
								{/if}
								<select bind:value={g.access} aria-label="Access">
									<option value="read">Read</option>
									<option value="write">Write</option>
									<option value="both">Read and write</option>
								</select>
								<button class="btn small" type="button" onclick={() => dropGrant(i)}>Remove<span class="sr-only"> this line</span></button>
							</div>
						{/each}
						<div class="adders">
							<button class="btn small" type="button" onclick={() => addGrant('files')}>Add files</button>
							<button class="btn small" type="button" onclick={() => addGrant('db')} disabled={apps.length === 0}>Add a database</button>
							<button class="btn small" type="button" onclick={() => addGrant('data')} disabled={apps.length === 0}>Add app data</button>
						</div>
					</fieldset>

					<label for="key-end">Stops working on (optional)</label>
					<input id="key-end" type="date" bind:value={endDate} />

					{#if formError}<p class="notice bad" role="alert">{formError}</p>{/if}
					<div class="row-actions"><button class="btn primary" type="submit" disabled={sending}>{sending ? 'Making…' : 'Make key'}</button></div>
				</form>
			{/if}
		</section>

		<section class="card" aria-labelledby="app-h">
			<h2 id="app-h">Apps' own keys</h2>
			<p class="muted small none">Apps get these automatically as HOMELAB_TOKEN.</p>
			{#if appKeys.length > 0}
				<ul>
					{#each appKeys as k (k.id)}
						<li>
							<div class="info"><strong>{k.app ?? k.name}{k.env ? ` · ${k.env}` : ''}</strong></div>
							<span class="muted small">{#if k.lastUsed}<Time at={k.lastUsed} />{:else}Never used{/if}</span>
						</li>
					{/each}
				</ul>
			{/if}
		</section>
	{/if}

	{#if revoking}
		<Sheet title="Revoke {revoking.name}?" onclose={closeRevoke}>
			{#snippet children()}
				<p>Anything using this key will stop working straight away. This can't be undone.</p>
				{#if revokeError}<p class="notice bad" role="alert">{revokeError}</p>{/if}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={closeRevoke}>Keep key</button>
				<button class="btn danger" type="button" disabled={revokeBusy} onclick={revoke}>{revokeBusy ? 'Revoking…' : 'Revoke key'}</button>
			{/snippet}
		</Sheet>
	{/if}
</div>

<style>
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
		margin: 0 0 0.4rem;
		font-size: 1.1rem;
	}
	.none {
		margin: 0.25rem 0;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		padding: 0.7rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	.info {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.info p {
		margin: 0.15rem 0 0;
	}
	fieldset {
		border: 0;
		padding: 0;
		margin: 0.75rem 0 0;
	}
	legend {
		font-weight: 600;
		padding: 0;
	}
	.check {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-height: 44px;
		margin: 0;
		font-weight: 400;
	}
	.check input {
		width: 1.2rem;
		height: 1.2rem;
	}
	.grant {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.4rem;
		padding: 0.5rem 0;
		border-top: 1px solid var(--border);
	}
	.grant .kind {
		font-weight: 600;
		min-width: 5rem;
	}
	.grant input,
	.grant select {
		flex: 1 1 8rem;
		width: auto;
	}
	.adders,
	.row-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.6rem;
	}
	input[type='date'] {
		font: inherit;
		width: 100%;
		min-height: 44px;
		padding: 0.4rem 0.7rem;
		border-radius: 10px;
		border: 1px solid var(--border-strong);
		background: var(--bg);
		color: var(--text);
	}
	.secret {
		display: block;
		margin-top: 0.4rem;
		padding: 0.5rem 0.6rem;
		background: var(--bg);
		border: 1px solid var(--border-strong);
		border-radius: 8px;
		overflow-wrap: anywhere;
		user-select: all;
	}
	form .notice,
	.row-actions + .notice {
		margin-top: 0.75rem;
	}
</style>
