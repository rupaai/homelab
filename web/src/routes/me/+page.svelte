<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { MeView } from '#lib/types.ts';
	import { onMount } from 'svelte';
	import { disablePush, enablePush, pushEnabled, pushUnsupported } from '#lib/push.ts';

	let person = $state<MeView | null>(null);
	let error = $state('');
	let busy = $state(false);
	let passkeyUrl = $state('');
	let pushOn = $state(false);
	let pushBusy = $state(false);
	let pushNote = $state('');
	let pushError = $state('');
	const unsupported = pushUnsupported();
	onMount(async () => {
		try { person = await api.me(); } catch (e) { error = messageOf(e); }
		try { pushOn = await pushEnabled(); } catch { pushOn = false; }
	});
	async function togglePush() {
		pushBusy = true; pushError = ''; pushNote = '';
		try {
			if (pushOn) { await disablePush(); pushOn = false; }
			else { await enablePush(); pushOn = true; pushNote = 'Notifications are on for this device.'; }
		} catch (e) { pushError = messageOf(e); }
		finally { pushBusy = false; }
	}
	async function testPush() {
		pushBusy = true; pushError = ''; pushNote = '';
		try { await api.testPush(); pushNote = 'Sent. It should arrive in a few seconds.'; }
		catch (e) { pushError = messageOf(e); }
		finally { pushBusy = false; }
	}
	async function setUpPasskey() {
		busy = true; error = ''; passkeyUrl = '';
		try { passkeyUrl = (await api.passkeyLink()).url; }
		catch (e) { error = messageOf(e); }
		finally { busy = false; }
	}
	async function signOut() {
		busy = true; error = '';
		try { await api.signOut(); window.location.assign('/'); }
		catch (e) { error = messageOf(e); busy = false; }
	}
</script>

<svelte:head><title>Me · Homelab</title></svelte:head>

<div class="page">
	<h1>Me</h1>
	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{#if person}<section class="card"><h2>{person.name}</h2><p class="muted">{person.login}</p><p>Roles: {person.roles.join(', ')}</p>{#if person.local}<button class="btn" type="button" disabled={busy} onclick={setUpPasskey}>{busy ? 'Preparing…' : 'Set up a passkey'}</button>{#if passkeyUrl}<p>Pocket ID will open its passkey setup page. On that page, click Add passkey to register this device.</p><a class="btn primary" href={passkeyUrl} target="_blank" rel="noopener">Register passkey</a>{/if}{/if}<button class="btn danger" type="button" disabled={busy} onclick={signOut}>{busy ? 'Signing out…' : 'Sign out'}</button></section>{/if}
	{#if person}
		<section class="card" aria-labelledby="notifications">
			<h2 id="notifications">Notifications</h2>
			<p class="muted">Homelab tells you when an app you look after goes down or is rolled back{#if person.roles.some((r) => r === 'owner' || r === 'admin')}, and when the disk is almost full{/if}. Turn this on for each phone or computer.</p>
			{#if unsupported}<p class="notice">{unsupported}</p>{:else}
				<div class="actions"><button class="btn {pushOn ? '' : 'primary'}" type="button" disabled={pushBusy} onclick={togglePush}>{pushBusy ? 'Working…' : pushOn ? 'Turn off on this device' : 'Turn on for this device'}</button>{#if pushOn}<button class="btn" type="button" disabled={pushBusy} onclick={testPush}>Send a test</button>{/if}</div>
			{/if}
			{#if pushNote}<p class="muted" role="status">{pushNote}</p>{/if}
			{#if pushError}<p class="notice bad" role="alert">{pushError}</p>{/if}
		</section>
	{/if}
</div>

<style>
	.card + .card { margin-top: 1rem; }
	.actions { display: flex; flex-wrap: wrap; gap: 0.5rem; margin: 0.8rem 0; }
</style>
