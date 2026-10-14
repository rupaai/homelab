<script lang="ts">
	import { healthLabel } from '#lib/format.ts';
	import type { Health } from '#lib/types.ts';

	let { health, label }: { health: Health | string; label?: string } = $props();
	const known = $derived(['healthy', 'degraded', 'down'].includes(health) ? health : 'unknown');
</script>

<span class="status {known}"><span class="dot" aria-hidden="true"></span>{label ?? healthLabel(health)}</span>

<style>
	.status {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-weight: 600;
		white-space: nowrap;
	}
	.dot {
		width: 0.65rem;
		height: 0.65rem;
		border-radius: 50%;
		background: currentColor;
		flex: none;
	}
	.healthy {
		color: var(--ok);
	}
	.degraded {
		color: var(--warn);
	}
	.down {
		color: var(--bad);
	}
	.unknown {
		color: var(--muted);
	}
	/* The dot's shape differs too, so it isn't only colour. */
	.degraded .dot {
		border-radius: 2px;
		transform: rotate(45deg) scale(0.9);
	}
	.down .dot {
		border-radius: 2px;
	}
	.unknown .dot {
		background: transparent;
		border: 2px solid currentColor;
	}
</style>
