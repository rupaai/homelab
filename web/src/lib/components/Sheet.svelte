<script lang="ts">
	import { onMount, type Snippet } from 'svelte';

	// A modal dialog. The native <dialog> gives us the focus trap, Escape and inert page.
	let {
		title,
		onclose,
		children,
		footer
	}: { title: string; onclose: () => void; children: Snippet; footer?: Snippet } = $props();

	let dlg: HTMLDialogElement;
	const id = `sheet-${Math.random().toString(36).slice(2, 8)}`;

	onMount(() => {
		dlg.showModal();
		return () => dlg.open && dlg.close();
	});
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<dialog bind:this={dlg} aria-labelledby={id} {onclose} onclick={(e) => e.target === dlg && dlg.close()}>
	<div class="panel">
		<header>
			<h2 {id}>{title}</h2>
			<button class="icon" type="button" aria-label="Close" onclick={() => dlg.close()}>
				<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
			</button>
		</header>
		<div class="body">{@render children()}</div>
		{#if footer}<footer>{@render footer()}</footer>{/if}
	</div>
</dialog>

<style>
	dialog {
		border: 0;
		padding: 0;
		background: transparent;
		color: inherit;
		width: 100%;
		max-width: 34rem;
		max-height: 100dvh;
		margin: auto auto 0;
		overflow: visible;
	}
	dialog::backdrop {
		background: rgb(0 0 0 / 0.5);
	}
	.panel {
		background: var(--surface);
		border-radius: 18px 18px 0 0;
		max-height: min(92dvh, 46rem);
		display: flex;
		flex-direction: column;
		box-shadow: var(--shadow);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.9rem 1rem 0.5rem 1.1rem;
	}
	h2 {
		margin: 0;
		font-size: 1.15rem;
	}
	.icon {
		display: grid;
		place-items: center;
		width: 44px;
		height: 44px;
		border: 0;
		border-radius: 10px;
		background: transparent;
		color: var(--muted);
		cursor: pointer;
	}
	.icon:hover {
		background: var(--surface-2);
		color: var(--text);
	}
	.body {
		padding: 0.25rem 1.1rem 1rem;
		overflow-y: auto;
		overscroll-behavior: contain;
	}
	footer {
		display: flex;
		gap: 0.6rem;
		justify-content: flex-end;
		padding: 0.8rem 1.1rem calc(0.8rem + env(safe-area-inset-bottom));
		border-top: 1px solid var(--border);
	}
	@media (min-width: 600px) {
		dialog {
			margin: auto;
		}
		.panel {
			border-radius: 18px;
		}
		footer {
			padding-bottom: 0.8rem;
		}
	}
</style>
