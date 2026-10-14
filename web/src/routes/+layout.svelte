<script lang="ts">
	import { page } from '$app/state';
	import { appsStore } from '#lib/apps.svelte.ts';
	import '../app.css';

	let { children } = $props();

	const tabs = [
		{ href: '/', label: 'Projects', match: (p: string) => p === '/' || p.startsWith('/apps'), icon: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z' },
		{ href: '/activity', label: 'Activity', match: (p: string) => p.startsWith('/activity'), icon: 'M4 12h4l3-8 4 16 3-8h2' },
		{ href: '/settings', label: 'Settings', match: (p: string) => p.startsWith('/settings'), icon: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z' }
	];
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
		{#if appsStore.error && appsStore.loaded}
			<p class="notice bad offline" role="alert">{appsStore.error}</p>
		{/if}
		{@render children()}
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
		grid-template-columns: repeat(3, 1fr);
		border-top: 1px solid var(--border);
		background: var(--surface);
		padding-bottom: env(safe-area-inset-bottom);
	}
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
