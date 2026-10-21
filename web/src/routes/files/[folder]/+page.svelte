<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { bytes } from '#lib/format.ts';
	import { isImage, parseTags, saveBlob } from '#lib/files.ts';
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

	async function load(after?: string) {
		const tag = tagFilter.trim();
		try {
			const r = await api.files(folder, { after, tag: tag || undefined });
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
	async function upload() {
		const chosen = Array.from(picker.files ?? []);
		if (chosen.length === 0) return;
		const tags = parseTags(tagText);
		uploading = true;
		uploadErrors = [];
		let saved = 0;
		for (const [i, f] of chosen.entries()) {
			progress = `Saving ${i + 1} of ${chosen.length}: ${f.name}`;
			try {
				await api.uploadFile(folder, f, tags);
				saved++;
			} catch (e) {
				uploadErrors = [...uploadErrors, `${f.name}: ${messageOf(e)}`];
			}
		}
		picker.value = '';
		progress = saved > 0 ? `Saved ${saved} of ${chosen.length}.` : '';
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
			{#if files.length === 0}
				<p class="muted none">{tagFilter.trim() ? 'No files have that tag.' : 'No files yet. Add some above.'}</p>
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
							</div>
							<div class="acts">
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

	{#if previewing}
		<Sheet title={previewing.name} onclose={() => (previewing = null)}>
			{#snippet children()}
				<FileImage {folder} id={previewing!.id} name={previewing!.name} large />
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={() => previewing && download(previewing)}>Download</button>
				<button class="btn primary" type="button" onclick={() => (previewing = null)}>Close</button>
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
