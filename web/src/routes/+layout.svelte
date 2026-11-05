<script lang="ts">
	import { onMount } from 'svelte';
	import { onSessionLost } from '#lib/session.ts';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { copyText } from '#lib/clipboard.ts';
	import { takeFragmentToken } from '#lib/token.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { ApiError, messageOf } from '#lib/errors.ts';
	import PasskeyRegistration from '#lib/components/PasskeyRegistration.svelte';
	import type { MeView, SetupResult } from '#lib/types.ts';
	import { navTabs } from '#lib/nav.ts';
	import '../app.css';

	let { children } = $props();
	let authState = $state<'loading'|'setup'|'setupDone'|'recoveryDone'|'inviteDone'|'invitePassword'|'inviteError'|'signedOut'|'password'|'ready'|'error'>('loading');
	let me = $state<MeView | null>(null);
	let setupName = $state('');
	let setupEmail = $state('');
	let setupResult = $state<SetupResult | null>(null);
	let recoveryCode = $state('');
	let password = $state('');
	let email = $state('');
	let newPassword = $state('');
	let repeatPassword = $state('');
	let passwordReady = $state(true);
	let passwordMode = false;
	let authError = $state('');
	let busy = $state(false);
	let recoverySaved = $state(false);
	let copied = $state(false);
	let recoveryPasskeyUrl = $state('');
	let initialOwnerPasskeyUrl = $state('');
	let ownerNeedsPasskey = $state(false);
	let invitePasskeyUrl = $state('');
	let inviteToken = '';
	const tabs = $derived(navTabs(me?.roles ?? []));
	// One tab is not a menu: family and builders see no tab bar.
	const showNav = $derived(authState === 'ready' && tabs.length > 1);
	const initial = $derived((me?.name?.trim().charAt(0) || '?').toUpperCase());

	onMount(() => {
		// A session that ends while the app is open (a Home Screen app can stay open for days).
		const stop = onSessionLost(() => {
			if (authState === 'ready') { me = null; authState = passwordMode ? 'password' : 'signedOut'; }
		});
		start();
		return stop;
	});

	async function start() {
		takeFragmentToken();
		const invite = page.url.pathname.match(/^\/invite\/([^/]+)\/?$/);
		if (invite) {
			inviteToken = invite[1];
			// On another device (addresses: ip) a passkey can't be made, so they choose a password.
			const onNetwork = await api.bootstrap().then((s) => !!s.password, () => false);
			if (onNetwork) { authState = 'invitePassword'; return; }
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
			passwordReady = status.signInReady;
			passwordMode = !!status.password;
			try {
				me = await api.me(); authState = 'ready';
			} catch (e) { authState = e instanceof ApiError && e.status === 403 ? (passwordMode ? 'password' : 'signedOut') : 'error'; }
		} catch { authState = 'error'; }
	}

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
	async function passwordSignIn(e: SubmitEvent) {
		e.preventDefault(); busy = true; authError = '';
		try { await api.passwordSignIn(password, email.trim() || undefined); password = ''; location.reload(); }
		catch (e) { authError = messageOf(e); }
		finally { busy = false; }
	}
	async function chooseInvitePassword(e: SubmitEvent) {
		e.preventDefault(); authError = '';
		if (newPassword.length < 10) { authError = 'Use at least 10 characters.'; return; }
		if (newPassword !== repeatPassword) { authError = "The two passwords don't match."; return; }
		busy = true;
		try { await api.setInvitePassword(inviteToken, newPassword); newPassword = repeatPassword = ''; location.assign('/'); }
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
		me = null; authState = location.hostname === 'localhost' || location.hostname === '127.0.0.1' ? 'signedOut' : 'password'; await goto('/');
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
		copied = await copyText(setupResult.recoveryCode);
		if (!copied) authError = 'Your browser could not copy the code. Select and copy it instead.';
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
			<span class="logo" aria-hidden="true">
				<svg viewBox="0 0 24 24" width="20" height="20"><path d="M5 11.2 12 5l7 6.2V19H5z" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linejoin="round" /><path d="M10 19v-4.5h4V19" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linejoin="round" /></svg>
			</span>
			<span>Homelab</span>
		</a>
		{#if showNav}
			<nav class="top" aria-label="Main">
				{#each tabs as t}
					<a href={t.href} aria-current={t.match(page.url.pathname) ? 'page' : undefined}>
						<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={t.icon} /></svg>
						<span>{t.label}</span>
					</a>
				{/each}
			</nav>
		{/if}
		{#if authState === 'ready' && me}
			<a class="avatar" href="/me" aria-label="Your account" aria-current={page.url.pathname.startsWith('/me') ? 'page' : undefined}><span>{initial}</span></a>
		{/if}
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
		{:else if authState === 'invitePassword'}
			<div class="auth-card"><h1>Choose a password</h1><p>You’ll use it with your email address to sign in to Homelab.</p><form onsubmit={chooseInvitePassword}><label for="new-password">Password</label><input id="new-password" type="password" bind:value={newPassword} autocomplete="new-password" minlength="10" required /><label for="repeat-password">Repeat password</label><input id="repeat-password" type="password" bind:value={repeatPassword} autocomplete="new-password" minlength="10" required /><p class="muted">At least 10 characters.</p>{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<button class="btn primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save password'}</button></form></div>
		{:else if authState === 'inviteError'}
			<div class="auth-card"><h1>This invite can’t be used</h1><p class="notice bad" role="alert">{authError}</p><p>Ask the Homelab owner to send a new invite.</p></div>
		{:else if authState === 'password'}
			<div class="auth-card"><h1>Sign in to Homelab</h1>{#if passwordReady}<form onsubmit={passwordSignIn}><label for="email">Email <span class="muted">(leave empty if you’re the owner)</span></label><input id="email" type="email" bind:value={email} autocomplete="username" /><label for="password">Password</label><input id="password" type="password" bind:value={password} autocomplete="current-password" required />{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<button class="btn primary" type="submit" disabled={busy}>{busy ? 'Checking…' : 'Sign in'}</button></form>{:else}<p>No password is set yet. On the device, run:</p><p><code>sudo -u homelab /var/lib/homelab/bin/homelabd password --home /var/lib/homelab</code></p><button class="btn" type="button" onclick={() => location.reload()}>Try again</button>{/if}</div>
		{:else if authState === 'signedOut'}
			<div class="auth-card"><h1>Sign in to Homelab</h1><p>Use the passkey you set up with Pocket ID.</p>{#if ownerNeedsPasskey}<p>Passkey setup for the owner hasn’t completed. You can request a fresh Pocket ID setup link here.</p>{#if initialOwnerPasskeyUrl}<PasskeyRegistration url={initialOwnerPasskeyUrl} continueLabel="Continue to sign in" onContinue={() => location.assign('/auth/start')} />{:else}<button class="btn primary" type="button" onclick={prepareInitialOwnerPasskeyLink} disabled={busy}>{busy ? 'Preparing…' : 'Set up passkey'}</button>{/if}{/if}{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<a class="btn" href="/auth/start" data-sveltekit-reload>Sign in with passkey</a><details><summary>Use your recovery code</summary><form onsubmit={recover}><label for="recovery-code">Recovery code</label><input id="recovery-code" bind:value={recoveryCode} autocomplete="off" required />{#if authError}<p class="notice bad" role="alert">{authError}</p>{/if}<button class="btn" type="submit" disabled={busy}>{busy ? 'Checking…' : 'Sign in once'}</button></form></details></div>
		{:else}
		{#if appsStore.error && appsStore.loaded}
			<p class="notice bad offline" role="alert">{appsStore.error}</p>
		{/if}
		{@render children()}
		{/if}
	</main>

	{#if showNav}
		<nav class="tabbar" aria-label="Main">
			{#each tabs as t}
				<a href={t.href} aria-current={t.match(page.url.pathname) ? 'page' : undefined}>
					<svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={t.icon} /></svg>
					<span>{t.label}</span>
				</a>
			{/each}
		</nav>
	{/if}
</div>

<style>
	.shell {
		height: 100dvh;
		display: grid;
		grid-template-rows: auto minmax(0, 1fr) auto;
		max-width: 64rem;
		margin: 0 auto;
	}
	header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-height: 56px;
		padding: env(safe-area-inset-top) calc(var(--gutter) + env(safe-area-inset-right)) 0 calc(var(--gutter) + env(safe-area-inset-left));
		border-bottom: 1px solid var(--border);
		background: var(--bg);
	}
	.brand {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		margin-right: auto;
		font-weight: 600;
		font-size: 1rem;
		letter-spacing: -0.01em;
		color: var(--text);
		text-decoration: none;
	}
	.logo {
		width: 28px;
		height: 28px;
		border-radius: 8px;
		border: 1px solid var(--border-strong);
		background: var(--surface);
		display: grid;
		place-items: center;
		color: var(--accent);
	}
	.top {
		display: none;
		gap: 0.25rem;
	}
	.top a {
		position: relative;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-height: 44px;
		padding: 0 0.75rem;
		color: var(--muted);
		text-decoration: none;
		font-size: 0.875rem;
		font-weight: 500;
	}
	.top a:hover {
		color: var(--text);
	}
	.top a[aria-current='page'] {
		color: var(--accent);
	}
	.top a[aria-current='page']::after {
		content: '';
		position: absolute;
		left: 0.75rem;
		right: 0.75rem;
		bottom: -6px;
		height: 2px;
		background: var(--accent);
	}
	.avatar {
		width: 44px;
		height: 44px;
		display: grid;
		place-items: center;
		text-decoration: none;
		color: var(--text);
	}
	.avatar span {
		width: 32px;
		height: 32px;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--surface-2);
		border: 1px solid var(--border-strong);
		font-size: 0.8125rem;
		font-weight: 600;
	}
	.avatar[aria-current='page'] span {
		border-color: var(--accent);
		color: var(--accent);
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
		background: var(--bg);
		padding-bottom: env(safe-area-inset-bottom);
	}
	.tabbar a {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 2px;
		min-height: 56px;
		color: var(--muted);
		font-size: 0.6875rem;
		font-weight: 500;
		text-decoration: none;
	}
	.tabbar a[aria-current='page'] {
		color: var(--accent);
	}
	.auth-card { max-width: 29rem; margin: 2.5rem var(--gutter); padding: 1.5rem; border: 1px solid var(--border); border-radius: var(--radius); background: var(--surface); }
	.auth-card h1 { margin: 0 0 .5rem; }
	.auth-card p { color: var(--muted); }
	.auth-card p strong, .auth-card p code { color: var(--text); }
	.auth-card form { display: grid; gap: .5rem; margin-top: 1rem; }
	.auth-card input { width: 100%; }
	.auth-card .btn { width: fit-content; }
	.auth-card .notice { color: var(--text); }
	.auth-card details { margin-top: 1.5rem; }
	.auth-card summary { cursor: pointer; font-weight: 500; min-height: 44px; display: flex; align-items: center; }
	.recovery-code { border: 1px solid var(--border-strong); border-radius: 8px; padding: 1rem; background: var(--surface-2); font-family: var(--mono); overflow-wrap: anywhere; }
	.actions { display: flex; flex-wrap: wrap; gap: .5rem; margin: .8rem 0; }
	@media (min-width: 720px) {
		header {
			padding-left: calc(24px + env(safe-area-inset-left));
			padding-right: calc(24px + env(safe-area-inset-right));
			min-height: 60px;
		}
		.tabbar {
			display: none;
		}
		.top {
			display: flex;
			margin-right: 0.5rem;
		}
		.auth-card {
			margin-left: auto;
			margin-right: auto;
		}
		.shell {
			grid-template-rows: auto minmax(0, 1fr);
		}
	}
</style>
