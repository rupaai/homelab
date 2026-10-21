<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { bytes } from '#lib/format.ts';
	import { isImage, metaLines, metaSummary, parseTags, saveBlob } from '#lib/files.ts';
	import type { StoredFile } from '#lib/types.ts';
	import FileImage from '#lib/components/FileImage.svelte';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';
	import Time from '#lib/components/Time.svelte';

	const folder = $derived(page.params.folder ?? '');

	let files = $state<StoredFile[]>([]);
	let next = $state<string | null>(null);
	let loaded = $state(false);
	let more = $state(false);
	let error = $state('');
	let tagFilter = $state('');
	let takenFrom = $state('');
	let takenTo = $state('');
	let cameraFilter = $state('');
	const filtered = $derived(!!(tagFilter.trim() || takenFrom || takenTo || cameraFilter.trim()));

	async function load(after?: string) {
		const tag = tagFilter.trim();
		try {
			const r = await api.files(folder, { after, tag: tag || undefined, takenAfter: takenFrom || undefined, takenBefore: takenTo || undefined, camera: cameraFilter.trim() || undefined });
			files = after ? [...files, ...r.files] : r.files;
			next = r.next;
			error = '';
		} catch (e) {
			error = messageOf(e);
		} finally {
			loaded = true;
			more = false;
		}
	}
	onMount(() => load());
	function showMore() {
		if (!next) return;
		more = true;
		load(next);
	}
	const filter = (e: Event) => {
		e.preventDefault();
		load();
	};

	// Upload
	let picker: HTMLInputElement;
	let tagText = $state('');
	let progress = $state('');
	let uploadErrors = $state<string[]>([]);
	let uploading = $state(false);
	let duplicates = $state<string[]>([]);
	async function upload() {
		const chosen = Array.from(picker.files ?? []);
		if (chosen.length === 0) return;
		const tags = parseTags(tagText);
		uploading = true;
		uploadErrors = [];
		duplicates = [];
		let saved = 0;
		let already: string[] = [];
		for (const [i, f] of chosen.entries()) {
			progress = `Saving ${i + 1} of ${chosen.length}: ${f.name}`;
			try {
				const r = await api.uploadFile(folder, f, tags);
				if (r.duplicate) already = [...already, r.file.name];
				else saved++;
			} catch (e) {
				uploadErrors = [...uploadErrors, `${f.name}: ${messageOf(e)}`];
			}
		}
		picker.value = '';
		progress = saved > 0 ? `Saved ${saved} of ${chosen.length}.` : '';
		duplicates = already;
		uploading = false;
		if (saved > 0) await load();
	}

	// Download, preview
	let downloadError = $state('');
	async function download(f: StoredFile) {
		downloadError = '';
		try {
			saveBlob(await api.fileBlob(folder, f.id), f.name);
		} catch (e) {
			downloadError = messageOf(e);
		}
	}
	let previewing = $state<StoredFile | null>(null);

	// Delete
	let deleting = $state<StoredFile | null>(null);
	let deleteError = $state('');
	let deleteBusy = $state(false);
	async function remove() {
		if (!deleting) return;
		deleteBusy = true;
		deleteError = '';
		try {
			await api.deleteFile(folder, deleting.id);
			deleting = null;
			await load();
		} catch (e) {
			deleteError = messageOf(e);
		} finally {
			deleteBusy = false;
		}
	}
	const closeDelete = () => ((deleting = null), (deleteError = ''));

	// Delete the folder (only when it is empty)
	let deletingFolder = $state(false);
	let folderError = $state('');
	let folderBusy = $state(false);
	const canDeleteFolder = $derived(loaded && !error && files.length === 0 && !filtered);
	async function removeFolder() {
		folderBusy = true;
		folderError = '';
		try {
			await api.deleteFileFolder(folder);
			deletingFolder = false;
			await goto('/files');
		} catch (e) {
			folderError = messageOf(e);
		} finally {
			folderBusy = false;
		}
	}
	const closeFolder = () => ((deletingFolder = false), (folderError = ''));
</script>

<svelte:head><title>{folder} · Files · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><h1>{folder}</h1><a class="btn small" href="/files">All folders</a></div>

	<section class="card" aria-labelledby="up-h">
		<h2 id="up-h">Add files</h2>
		<label for="up-tags">Tags (optional, with commas)</label>
		<input id="up-tags" type="text" bind:value={tagText} autocomplete="off" placeholder="trip, 2026" />
		<input class="hidden" type="file" multiple bind:this={picker} onchange={upload} aria-label="Choose files" />
		<div class="row-actions"><button class="btn primary" type="button" disabled={uploading} onclick={() => picker.click()}>{uploading ? 'Saving…' : 'Choose files'}</button></div>
		{#if progress}<p class="muted small" role="status">{#if uploading}<Spinner /> {/if}{progress}</p>{/if}
		{#each duplicates as n}<p class="muted small" role="status">Already here: {n}</p>{/each}
		{#each uploadErrors as m}<p class="notice bad" role="alert">{m}</p>{/each}
	</section>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{#if downloadError}<p class="notice bad" role="alert">{downloadError}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if !error || files.length > 0}
		<section class="card" aria-labelledby="files-h">
			<h2 id="files-h">Files</h2>
			<form class="filter" onsubmit={filter}>
				<label class="sr-only" for="tag-filter">Show only this tag</label>
				<input id="tag-filter" type="text" bind:value={tagFilter} autocomplete="off" placeholder="Show only a tag" />
				<button class="btn small" type="submit">Show</button>
			</form>
			<form class="filter more-filters" onsubmit={filter}>
				<div><label for="taken-from">Taken from</label><input id="taken-from" type="date" bind:value={takenFrom} /></div>
				<div><label for="taken-to">to</label><input id="taken-to" type="date" bind:value={takenTo} /></div>
				<div class="grow"><label for="camera-filter">Camera</label><input id="camera-filter" type="text" bind:value={cameraFilter} autocomplete="off" placeholder="iPhone" /></div>
			</form>
			{#if files.length === 0}
				<p class="muted none">{filtered ? 'No files match.' : 'No files yet. Add some above.'}</p>
			{:else}
				<ul>
					{#each files as f (f.id)}
						<li>
							{#if isImage(f.content_type)}
								<button class="thumb" type="button" onclick={() => (previewing = f)} aria-label="Preview {f.name}"><FileImage {folder} id={f.id} name="" /></button>
							{/if}
							<div class="info">
								<strong>{f.name}</strong>
								<p class="muted small">{bytes(f.size)} · <Time at={f.created_at} />{#if f.tags.length} · {f.tags.join(', ')}{/if}</p>
								{#if metaSummary(f.meta)}<p class="muted small">{metaSummary(f.meta)}</p>{/if}
							</div>
							<div class="acts">
								{#if !isImage(f.content_type) && metaLines(f.meta).length}<button class="btn small" type="button" onclick={() => (previewing = f)}>Details<span class="sr-only"> {f.name}</span></button>{/if}
								<button class="btn small" type="button" onclick={() => download(f)}>Download<span class="sr-only"> {f.name}</span></button>
								<button class="btn danger small" type="button" onclick={() => (deleting = f)}>Delete<span class="sr-only"> {f.name}</span></button>
							</div>
						</li>
					{/each}
				</ul>
				{#if next}<div class="row-actions"><button class="btn" type="button" disabled={more} onclick={showMore}>{more ? 'Loading…' : 'Show more'}</button></div>{/if}
			{/if}
		</section>
	{/if}

	{#snippet details(f: StoredFile)}
		{@const lines = metaLines(f.meta)}
		{#if lines.length}
			<dl class="meta">
				{#each lines as l}
					<dt>{l.label}</dt>
					<dd>{l.value}{#if l.link} · <a href={l.link.href} target="_blank" rel="noopener noreferrer">{l.link.text}</a>{/if}</dd>
				{/each}
			</dl>
		{/if}
	{/snippet}

	{#if previewing}
		<Sheet title={previewing.name} onclose={() => (previewing = null)}>
			{#snippet children()}
				{#if isImage(previewing!.content_type)}<FileImage {folder} id={previewing!.id} name={previewing!.name} large />{/if}
				{@render details(previewing!)}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={() => previewing && download(previewing)}>Download</button>
				<button class="btn primary" type="button" onclick={() => (previewing = null)}>Close</button>
			{/snippet}
		</Sheet>
	{/if}

	{#if loaded}
		<section class="card" aria-labelledby="del-h">
			<h2 id="del-h">Delete folder</h2>
			<p class="muted small">{canDeleteFolder ? 'This folder is empty.' : 'Only an empty folder can be deleted. Delete its files first, and clear the filters.'}</p>
			<div class="row-actions"><button class="btn danger" type="button" disabled={!canDeleteFolder} onclick={() => (deletingFolder = true)}>Delete folder</button></div>
		</section>
	{/if}

	{#if deletingFolder}
		<Sheet title="Delete the folder {folder}?" onclose={closeFolder}>
			{#snippet children()}
				<p>Delete the folder {folder}? It's empty. Files in its trash are removed for good.</p>
				{#if folderError}<p class="notice bad" role="alert">{folderError}</p>{/if}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={closeFolder}>Keep folder</button>
				<button class="btn danger" type="button" disabled={folderBusy} onclick={removeFolder}>{folderBusy ? 'Deleting…' : 'Delete folder'}</button>
			{/snippet}
		</Sheet>
	{/if}

	{#if deleting}
		<Sheet title="Delete {deleting.name}?" onclose={closeDelete}>
			{#snippet children()}
				<p>The file goes to the trash. Homelab empties the trash after 30 days.</p>
				{#if deleteError}<p class="notice bad" role="alert">{deleteError}</p>{/if}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={closeDelete}>Keep file</button>
				<button class="btn danger" type="button" disabled={deleteBusy} onclick={remove}>{deleteBusy ? 'Deleting…' : 'Delete file'}</button>
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
	.hidden {
		display: none;
	}
	.row-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 0.6rem;
	}
	.filter {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.more-filters {
		flex-wrap: wrap;
		align-items: flex-end;
		margin-top: 0.5rem;
	}
	.grow {
		flex: 1;
		min-width: 8rem;
	}
	.meta {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.25rem 0.9rem;
		margin: 0.8rem 0 0;
	}
	.meta dt {
		color: var(--muted, inherit);
	}
	.meta dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	ul {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0;
	}
	li {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.7rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	.info {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.info p {
		margin: 0.15rem 0 0;
	}
	.thumb {
		padding: 0;
		border: 0;
		background: none;
		cursor: pointer;
		flex: none;
	}
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		justify-content: flex-end;
	}
</style>
