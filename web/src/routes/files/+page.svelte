<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { bytes } from '#lib/format.ts';
	import { fileCount, folderDate, folderOwner, folderProblem, freeText, usedPercent } from '#lib/files.ts';
	import type { FileFolder, StorageDisk } from '#lib/types.ts';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';
	import Time from '#lib/components/Time.svelte';

	let disks = $state<StorageDisk[]>([]);
	let folders = $state<FileFolder[]>([]);
	let loaded = $state(false);
	let error = $state('');

	async function load() {
		const [d, f] = await Promise.allSettled([api.storage(), api.fileFolders()]);
		if (d.status === 'fulfilled') disks = d.value;
		if (f.status === 'fulfilled') folders = f.value;
		const failed = d.status === 'rejected' ? d.reason : f.status === 'rejected' ? f.reason : null;
		error = failed ? messageOf(failed) : '';
		loaded = true;
	}
	onMount(load);

	const pickable = $derived(disks.filter((d) => d.kind === 'storage' && !d.shared));

	// New folder
	let making = $state(false);
	let name = $state('');
	let disk = $state('');
	let formError = $state('');
	let busy = $state(false);
	function open() {
		name = '';
		disk = '';
		formError = '';
		making = true;
	}
	async function create(e: Event) {
		e.preventDefault();
		formError = folderProblem(name);
		if (formError) return;
		busy = true;
		try {
			await api.createFileFolder(name, disk || undefined);
			making = false;
			await load();
		} catch (err) {
			formError = messageOf(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Files · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><h1>Files</h1><button class="btn primary small" type="button" onclick={open}>New folder</button></div>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else}
		<section class="card" aria-labelledby="disks-h">
			<h2 id="disks-h">Disks</h2>
			{#if disks.length === 0}
				<p class="muted none">Homelab hasn't found any disks.</p>
			{:else}
				<ul>
					{#each disks as d (d.name)}
						<li>
							<p class="line"><strong>{freeText(d)}</strong>{#if d.shared} <span class="chip">Shared</span>{/if}</p>
							<div class="bar" role="img" aria-label="{usedPercent(d)}% full"><span style="width: {usedPercent(d)}%" class:full={usedPercent(d) >= 90}></span></div>
							{#if d.problem}<p class="problem small">{d.problem}</p>{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</section>

		<section class="card" aria-labelledby="folders-h">
			<h2 id="folders-h">Folders</h2>
			{#if folders.length === 0}
				<p class="muted none">No folders yet. Make one to start saving files.</p>
			{:else}
				<ul>
					{#each folders as f (f.name)}
						<li>
							<a class="folder" href="/files/{encodeURIComponent(f.name)}">
								<strong>{f.name}</strong>
								<span class="muted small">{folderOwner(f)} · {fileCount(f.files)} · {bytes(f.bytes)} · on {f.disk}{#if folderDate(f.created_at)} · Made <Time at={folderDate(f.created_at)} />{/if}</span>
							</a>
						</li>
					{/each}
				</ul>
			{/if}
		</section>
	{/if}

	{#if making}
		<Sheet title="New folder" onclose={() => (making = false)}>
			{#snippet children()}
				<form id="new-folder" onsubmit={create}>
					<label for="folder-name">Name</label>
					<input id="folder-name" type="text" bind:value={name} maxlength="32" autocomplete="off" autocapitalize="none" spellcheck="false" placeholder="photos" />
					<p class="muted small hint">Small letters, numbers, dashes and underscores.</p>
					{#if pickable.length > 0}
						<label for="folder-disk">Disk</label>
						<select id="folder-disk" bind:value={disk}>
							<option value="">Homelab's own disk</option>
							{#each pickable as d (d.name)}<option value={d.name}>{freeText(d)}</option>{/each}
						</select>
					{/if}
					{#if formError}<p class="notice bad" role="alert">{formError}</p>{/if}
				</form>
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={() => (making = false)}>Cancel</button>
				<button class="btn primary" type="submit" form="new-folder" disabled={busy}>{busy ? 'Making…' : 'Make folder'}</button>
			{/snippet}
		</Sheet>
	{/if}
</div>

<style>
	.loading {
		margin: 0;
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	h2 {
		margin: 0 0 0.4rem;
		font-size: 1.1rem;
	}
	.none {
		margin: 0.25rem 0;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		padding: 0.6rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	.line {
		margin: 0 0 0.3rem;
	}
	.bar {
		height: 8px;
		border-radius: 4px;
		background: var(--border);
		overflow: hidden;
	}
	.bar span {
		display: block;
		height: 100%;
		background: var(--accent);
	}
	.bar span.full {
		background: var(--bad, #c0392b);
	}
	.problem {
		margin: 0.3rem 0 0;
		color: var(--bad, #c0392b);
	}
	.folder {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		text-decoration: none;
		color: var(--text);
		overflow-wrap: anywhere;
	}
	.hint {
		margin: 0.25rem 0 0;
	}
</style>
