<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { MeView } from '#lib/types.ts';
	import { onMount } from 'svelte';

	let person = $state<MeView | null>(null);
	let error = $state('');
	let busy = $state(false);
	onMount(async () => { try { person = await api.me(); } catch (e) { error = messageOf(e); } });
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
	{#if person}<section class="card"><h2>{person.name}</h2><p class="muted">{person.login}</p><p>Roles: {person.roles.join(', ')}</p><button class="btn danger" type="button" disabled={busy} onclick={signOut}>{busy ? 'Signing out…' : 'Sign out'}</button></section>{/if}
</div>
