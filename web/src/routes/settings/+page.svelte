<script lang="ts">
	const rows = [
		{ href: '/resources', title: 'This device', note: 'Health, updates and restarting Homelab.' },
		{ href: '/people', title: 'People', note: 'Who can use Homelab and its apps.' },
		{ href: '/settings/homelab', title: 'Homelab passwords and keys', note: 'Email setup and keys Homelab itself needs.' },
		{ href: '/settings/keys', title: 'API keys', note: 'Let a server outside Homelab use it.' },
		{ href: '/me', title: 'Your account', note: 'Passkey, notifications and sign out.' }
	];
</script>

<svelte:head><title>Settings · Homelab</title></svelte:head>

<div class="page">
	<h1>Settings</h1>
	<ul class="rows">
		{#each rows as r (r.href)}
			<li>
				<a class="card row" href={r.href}>
					<span class="text"><strong>{r.title}</strong><span class="muted small">{r.note}</span></span>
					<svg class="chev" viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
				</a>
			</li>
		{/each}
	</ul>
</div>

<style>
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.75rem;
	}
	.row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		min-height: 44px;
		color: var(--text);
		text-decoration: none;
	}
	.row:hover {
		background: var(--surface-2);
	}
	.text {
		flex: 1;
		min-width: 0;
		display: grid;
		gap: 0.1rem;
	}
	.chev {
		color: var(--muted);
		flex: none;
	}
</style>
