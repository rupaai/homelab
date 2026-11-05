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
		gap: 0.5rem;
		font-size: 0.875rem;
		font-weight: 500;
		white-space: nowrap;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: currentColor;
		flex: none;
	}
	.degraded {
		color: var(--warn);
	}
	.down {
		color: var(--bad);
	}
	.healthy {
		color: var(--muted);
	}
	.degraded,
	.down {
		font-weight: 600;
	}
	.unknown {
		color: var(--muted);
	}
	.healthy .dot {
		background: var(--ok);
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
		border: 1.5px solid currentColor;
	}
</style>
