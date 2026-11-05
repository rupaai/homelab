<script lang="ts">
	/** Two or three views of one tab, as links. The active one has aria-current="page". */
	let {
		views,
		current,
		label = 'View'
	}: { views: { href: string; label: string }[]; current: string; label?: string } = $props();
</script>

<nav class="switch" aria-label={label}>
	{#each views as v (v.href)}
		<a href={v.href} aria-current={v.href === current ? 'page' : undefined}>{v.label}</a>
	{/each}
</nav>

<style>
	.switch {
		display: flex;
		padding: 3px;
		gap: 3px;
		background: var(--chip);
		border-radius: 12px;
		width: 100%;
		max-width: 22rem;
	}
	a {
		flex: 1;
		min-height: 44px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0 1rem;
		border-radius: 9px;
		color: var(--muted);
		font-weight: 600;
		text-decoration: none;
	}
	a:hover {
		color: var(--text);
	}
	a[aria-current='page'] {
		background: var(--surface);
		color: var(--accent);
		box-shadow: 0 1px 3px rgb(0 0 0 / 0.15);
	}
</style>
