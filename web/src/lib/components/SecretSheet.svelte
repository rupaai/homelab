<script lang="ts">
	import { messageOf } from '#lib/errors.ts';
	import Sheet from './Sheet.svelte';

	// Asks for a new value for one secret. The value is sent, and not shown again here.
	let {
		title,
		name,
		intro,
		onsave,
		onclose
	}: { title: string; name: string; intro?: string; onsave: (value: string) => Promise<void>; onclose: () => void } = $props();

	let value = $state('');
	let reveal = $state(false);
	let error = $state('');
	let sending = $state(false);
	let input: HTMLInputElement;
	const inputId = `secret-${Math.random().toString(36).slice(2, 8)}`;

	$effect(() => input?.focus());

	async function save(e: Event) {
		e.preventDefault();
		if (!value) {
			error = 'Type a value first.';
			input.focus();
			return;
		}
		sending = true;
		error = '';
		try {
			await onsave(value);
		} catch (err) {
			error = messageOf(err);
			sending = false;
		}
	}
</script>

<Sheet {title} {onclose}>
	{#snippet children()}
		<form id="{inputId}-form" onsubmit={save}>
			{#if intro}<p>{intro}</p>{/if}
			<label for={inputId}>Value for <code>{name}</code></label>
			<div class="row">
				<input
					bind:this={input}
					id={inputId}
					type={reveal ? 'text' : 'password'}
					bind:value
					maxlength="8192"
					autocomplete="off"
					autocapitalize="off"
					spellcheck="false"
					aria-describedby="{inputId}-hint"
				/>
				<button class="btn" type="button" aria-pressed={reveal} onclick={() => (reveal = !reveal)}>{reveal ? 'Hide' : 'Show'}<span class="sr-only"> what I'm typing</span></button>
			</div>
			<p class="hint" id="{inputId}-hint">One line only. Homelab keeps the last 5 values in History.</p>
			{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
		</form>
	{/snippet}
	{#snippet footer()}
		<button class="btn" type="button" onclick={onclose}>Cancel</button>
		<button class="btn primary" type="submit" form="{inputId}-form" disabled={sending}>{sending ? 'Saving…' : 'Save'}</button>
	{/snippet}
</Sheet>

<style>
	.row {
		display: flex;
		gap: 0.5rem;
	}
	.row input {
		flex: 1;
		min-width: 0;
	}
	.notice {
		margin-top: 0.75rem;
	}
</style>
