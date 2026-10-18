<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { ApiError, messageOf } from '#lib/errors.ts';
	import PasskeyRegistration from '#lib/components/PasskeyRegistration.svelte';
	import type { MeView, SetupResult } from '#lib/types.ts';
	import '../app.css';

	let { children } = $props();
	let authState = $state<'loading'|'setup'|'setupDone'|'recoveryDone'|'inviteDone'|'inviteError'|'signedOut'|'ready'|'error'>('loading');
	let me = $state<MeView | null>(null);
	let setupName = $state('');
	let setupEmail = $state('');
	let setupResult = $state<SetupResult | null>(null);
	let recoveryCode = $state('');
	let authError = $state('');
	let busy = $state(false);
	let recoverySaved = $state(false);
	let copied = $state(false);
	let recoveryPasskeyUrl = $state('');
	let initialOwnerPasskeyUrl = $state('');
	let ownerNeedsPasskey = $state(false);
	let invitePasskeyUrl = $state('');
	let inviteToken = '';
	const admin = $derived(!!me?.roles.some((r) => r === 'owner' || r === 'admin'));
	const builder = $derived(admin || !!me?.roles.includes('builder'));
	const tabs = $derived.by(() => {
		const items: { href:string; label:string; match:(p:string)=>boolean; icon:string }[] = [
			{ href: '/home', label: 'Apps', match: (p: string) => p === '/home', icon: 'M3 5h7v6H3zM14 5h7v6h-7zM3 15h7v6H3zM14 15h7v6h-7z' }
		];
		if (builder) items.push({ href: '/', label: 'Projects', match: (p: string) => p === '/' || p.startsWith('/apps/'), icon: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z' });
		if (admin) items.push({ href: '/activity', label: 'Activity', match: (p: string) => p.startsWith('/activity'), icon: 'M4 12h4l3-8 4 16 3-8h2' });
		if (admin) items.push({ href: '/people', label: 'People', match: (p: string) => p.startsWith('/people'), icon: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8M20 8v6M23 11h-6' });
		if (admin) items.push({ href: '/settings', label: 'Settings', match: (p: string) => p.startsWith('/settings'), icon: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l-.1-.1a1.7 1.7 0 0 1 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z' });
		items.push({ href: '/me', label: 'Me', match: (p: string) => p.startsWith('/me'), icon: 'M20 21a8 8 0 0 0-16 0M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8' });
		return items;
	});

	onMount(async () => {
		const invite = page.url.pathname.match(/^\/invite\/([^/]+)\/?$/);
		if (invite) {
			inviteToken = invite[1];
			try {
				invitePasskeyUrl = (await api.redeemInvite(inviteToken)).url;
				authState = 'inviteDone';
			} catch (e) {
				authError = messageOf(e);
				authState = 'inviteError';
			}
			return;
		}
		try {
			const status = await api.bootstrap();
			ownerNeedsPasskey = status.ownerNeedsPasskey ?? false;
			if (status.needsSetup) { authState = 'setup'; return; }
			try {
				me = await api.me(); authState = 'ready';
				if (!me.roles.some((r) => ['owner', 'admin', 'builder'].includes(r)) && page.url.pathname === '/') await goto('/home', { replaceState: true });
			} catch (e) { authState = e instanceof ApiError && e.status === 403 ? 'signedOut' : 'error'; }
		} catch { authState = 'error'; }
	});

	async function setup(e: SubmitEvent) {
		e.preventDefault(); busy = true; authError = '';
		try { setupResult = await api.setupOwner(setupName.trim(), setupEmail.trim()); authState = 'setupDone'; }
		catch (e) { authError = messageOf(e); }
		finally { busy = false; }
	}
	async function recover(e: SubmitEvent) {
		e.preventDefault(); busy = true; authError = '';
		try {
			await api.recover(recoveryCode.trim());
			me = await api.me();
			authState = 'recoveryDone';
			recoveryPasskeyUrl = (await api.passkeyLink()).url;
		}
		catch (e) { authError = messageOf(e); }
		finally { busy = false; }
	}
	async function refreshRecoveryPasskeyLink() {
		busy = true; authError = ''; recoveryPasskeyUrl = '';
		try { recoveryPasskeyUrl = (await api.passkeyLink()).url; }
		catch (e) { authError = messageOf(e); }
		finally { busy = false; }
	}
	async function prepareInitialOwnerPasskeyLink() {
		busy = true; authError = ''; initialOwnerPasskeyUrl = '';
		try { initialOwnerPasskeyUrl = (await api.initialOwnerPasskeyLink()).url; }
		catch (e) { authError = messageOf(e); }
		finally { busy = false; }
	}
	async function signOut() {
		try { await api.signOut(); } catch (e) { authError = messageOf(e); return; }
		me = null; authState = 'signedOut'; await goto('/');
	}
	function continueFromInvite() {
		location.assign('/auth/start');
	}
	async function refreshInvitePasskeyLink() {
		if (!inviteToken) return;
		busy = true; authError = '';
		try { invitePasskeyUrl = (await api.redeemInvite(inviteToken)).url; }
		catch (e) { authError = messageOf(e); }
		finally { busy = false; }
	}
	async function copyRecovery() {
		if (!setupResult) return;
		try { await navigator.clipboard.writeText(setupResult.recoveryCode); copied = true; } catch { authError = 'Your browser could not copy the code. Select and copy it instead.'; }
	}
	function downloadRecovery() {
		if (!setupResult) return;
		const blob = new Blob([`Homelab recovery code\n\n${setupResult.recoveryCode}\n\nKeep this code private. It can sign in to Homelab once.\n`], { type: 'text/plain' });
		const url = URL.createObjectURL(blob); const a = document.createElement('a'); a.href = url; a.download = 'homelab-recovery-code.txt'; a.click(); URL.revokeObjectURL(url);
	}
</script>

<div class="shell">
	<header>
		<a class="brand" href="/">
			<img src="/icon.svg" alt="" width="26" height="26" />
			<span>Homelab</span>
		</a>
		<nav class="top" aria-label="Main">
			{#each tabs as t}
				<a href={t.href} aria-current={t.match(page.url.pathname) ? 'page' : undefined}>{t.label}</a>
			{/each}
		</nav>
	</header>

	<main>
		{#if authState === 'loading'}
			<div class="page"><p class="muted">Loading Homelab…</p></div>
		{:else if authState === 'error'}
			<div class="auth-card"><h1>Homelab is unavailable</h1><p>Check that Homelab is running, then try again.</p><button class="btn primary" type="button" onclick={() => location.reload()}>Try again</button></div>
		{:else if authState === 'setup'}
			<div class="auth-card"><h1>Set up Homelab</h1><p>Start by creating the household owner. You’ll register a passkey with Pocket ID next.</p><form onsubmit={setup}><label for="owner-name">Your name</label><input id="owner-name" bind:value={setupName} autocomplete="name" required /><label for="owner-email">Email</label><input id="owner-email" type="email" bind:value={setupEmail} autocomplete="email" required />{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<button class="btn primary" type="submit" disabled={busy}>{busy ? 'Setting up…' : 'Create owner account'}</button></form></div>
		{:else if authState === 'setupDone' && setupResult}
			<div class="auth-card"><h1>Save your recovery code</h1><p><strong>Save this now. It won’t be shown again.</strong> Keep it somewhere private.</p><p class="recovery-code"><code>{setupResult.recoveryCode}</code></p><div class="actions"><button class="btn" type="button" onclick={copyRecovery}>{copied ? 'Copied' : 'Copy code'}</button><button class="btn" type="button" onclick={downloadRecovery}>Download</button></div><PasskeyRegistration url={setupResult.loginUrl} continueLabel="Continue to sign in" onContinue={() => location.assign('/auth/start')} />{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}</div>
		{:else if authState === 'recoveryDone'}
			<div class="auth-card"><h1>Recovery complete</h1><p>{me?.name}, you’re signed in. Set up a passkey so you can sign in without your recovery code next time.</p>{#if recoveryPasskeyUrl}<PasskeyRegistration url={recoveryPasskeyUrl} continueLabel="Continue to Homelab" onContinue={() => location.assign('/')} />{:else}<button class="btn primary" type="button" onclick={refreshRecoveryPasskeyLink} disabled={busy}>{busy ? 'Preparing…' : 'Set up a passkey'}</button>{/if}{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}</div>
		{:else if authState === 'inviteDone'}
			<div class="auth-card"><h1>Set up your passkey</h1><p>Your invite is ready. Register a passkey with Pocket ID to finish setting up your Homelab account.</p><PasskeyRegistration url={invitePasskeyUrl} continueLabel="Continue to sign in" onContinue={continueFromInvite} onRefresh={refreshInvitePasskeyLink} refreshDisabled={busy} />{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}</div>
		{:else if authState === 'inviteError'}
			<div class="auth-card"><h1>This invite can’t be used</h1><p class="notice bad" role="alert">{authError}</p><p>Ask the Homelab owner to send a new invite, then open it on the Homelab computer.</p></div>
		{:else if authState === 'signedOut'}
			<div class="auth-card"><h1>Sign in to Homelab</h1><p>Use the passkey you set up with Pocket ID.</p>{#if ownerNeedsPasskey}<p>Passkey setup for the owner hasn’t completed. You can request a fresh Pocket ID setup link here.</p>{#if initialOwnerPasskeyUrl}<PasskeyRegistration url={initialOwnerPasskeyUrl} continueLabel="Continue to sign in" onContinue={() => location.assign('/auth/start')} />{:else}<button class="btn primary" type="button" onclick={prepareInitialOwnerPasskeyLink} disabled={busy}>{busy ? 'Preparing…' : 'Set up passkey'}</button>{/if}{/if}{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<a class="btn" href="/auth/start" data-sveltekit-reload>Sign in with passkey</a><details><summary>Use your recovery code</summary><form onsubmit={recover}><label for="recovery-code">Recovery code</label><input id="recovery-code" bind:value={recoveryCode} autocomplete="off" required />{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<button class="btn" type="submit" disabled={busy}>{busy ? 'Checking…' : 'Sign in once'}</button></form></details></div>
		{:else}
		{#if appsStore.error && appsStore.loaded}
			<p class="notice bad offline" role="alert">{appsStore.error}</p>
		{/if}
		{@render children()}
		{/if}
	</main>

	<nav class="tabbar" aria-label="Main">
		{#each tabs as t}
			<a href={t.href} aria-current={t.match(page.url.pathname) ? 'page' : undefined}>
				<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={t.icon} /></svg>
				<span>{t.label}</span>
			</a>
		{/each}
	</nav>
</div>

<style>
	.shell {
		height: 100dvh;
		display: grid;
		grid-template-rows: auto minmax(0, 1fr) auto;
		max-width: 44rem;
		margin: 0 auto;
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: calc(0.6rem + env(safe-area-inset-top)) calc(var(--gutter) + env(safe-area-inset-right)) 0.6rem calc(var(--gutter) + env(safe-area-inset-left));
		border-bottom: 1px solid var(--border);
		background: var(--surface);
	}
	.brand {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 700;
		font-size: 1.1rem;
		color: var(--text);
		text-decoration: none;
	}
	.top {
		display: none;
		gap: 0.25rem;
	}
	.top a {
		padding: 0.4rem 0.8rem;
		border-radius: 8px;
		color: var(--muted);
		text-decoration: none;
		font-weight: 600;
	}
	.top a[aria-current='page'] {
		color: var(--accent);
		background: var(--accent-soft);
	}
	main {
		overflow-y: auto;
		overscroll-behavior: contain;
		padding-left: env(safe-area-inset-left);
		padding-right: env(safe-area-inset-right);
	}
	.offline {
		margin: 0.75rem var(--gutter) 0;
	}
	.tabbar {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(3.25rem, 1fr));
		border-top: 1px solid var(--border);
		background: var(--surface);
		padding-bottom: env(safe-area-inset-bottom);
	}
	.auth-card { max-width: 29rem; margin: 2.5rem auto; padding: 1.4rem; border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface); }
	.auth-card h1 { margin: 0 0 .5rem; }
	.auth-card form { display: grid; gap: .55rem; margin-top: 1rem; }
	.auth-card input { width: 100%; }
	.auth-card .btn { width: fit-content; }
	.auth-card details { margin-top: 1.5rem; }
	.auth-card summary { cursor: pointer; font-weight: 600; }
	.recovery-code { border: 1px solid var(--border); border-radius: 8px; padding: 1rem; overflow-wrap: anywhere; }
	.actions { display: flex; flex-wrap: wrap; gap: .5rem; margin: .8rem 0; }
	.tabbar a {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.1rem;
		padding: 0.5rem 0 0.4rem;
		min-height: 52px;
		color: var(--muted);
		font-size: 0.75rem;
		font-weight: 600;
		text-decoration: none;
	}
	.tabbar a[aria-current='page'] {
		color: var(--accent);
	}
	@media (min-width: 720px) {
		.tabbar {
			display: none;
		}
		.top {
			display: flex;
		}
		.shell {
			grid-template-rows: auto minmax(0, 1fr);
		}
	}
</style>
