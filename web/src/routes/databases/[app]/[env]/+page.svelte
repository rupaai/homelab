<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { ApiError, messageOf } from '#lib/errors.ts';
	import { bytes } from '#lib/format.ts';
	import { PAGE_ROWS, PROD_COPY_NOTE, changeTime, changeWords, confirmLabel, copyKindWords, dbAddress, dbStatusWords, hasMoreChanges, loginProblem, needsConfirm, percent, personName, rowsRange, saveLogin, savedLogin, tableCount, tunnelCommand, writeHint } from '#lib/databases.ts';
	import { copyText } from '#lib/clipboard.ts';
	import type { AppDatabase, DbChange, DbCopy, EnvName, DbInfo, DbResult, DbRows, DbTable, PersonView } from '#lib/types.ts';
	import RevealedValue from '#lib/components/RevealedValue.svelte';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';
	import Time from '#lib/components/Time.svelte';

	const app = $derived(page.params.app ?? '');
	const env = $derived(page.params.env ?? '');

	let info = $state<DbInfo | null>(null);
	let tables = $state<DbTable[]>([]);
	let appDb = $state<AppDatabase | null>(null);
	let loaded = $state(false);
	let error = $state('');
	const CHANGES_PAGE = 50;
	let changes = $state<DbChange[]>([]);
	let moreChanges = $state(false);
	let changesBusy = $state(false);
	let changesError = $state('');
	let people = $state<PersonView[]>([]);

	async function loadMoreChanges() {
		changesBusy = true;
		changesError = '';
		try {
			const next = await api.dbChanges(app, env, changes[changes.length - 1]?.id, CHANGES_PAGE);
			changes = [...changes, ...next];
			moreChanges = hasMoreChanges(next.length, CHANGES_PAGE);
		} catch (e) {
			changesError = messageOf(e);
		} finally {
			changesBusy = false;
		}
	}

	// Import
	let picker = $state<HTMLInputElement | null>(null);
	let importFile = $state<File | null>(null);
	let importing = $state(false);
	let progress = $state(0);
	let importError = $state('');
	let importNote = $state('');
	function picked() {
		importFile = picker?.files?.[0] ?? null;
		importError = '';
		importNote = '';
	}
	function closeImport() {
		if (importing) return;
		importFile = null;
		importError = '';
		if (picker) picker.value = '';
	}
	async function doImport() {
		if (!importFile) return;
		importing = true;
		progress = 0;
		importError = '';
		try {
			await api.importDb(app, env, importFile, (d) => (progress = d));
			importNote = 'Started.';
			importFile = null;
			if (picker) picker.value = '';
			await load();
		} catch (e) {
			importError = messageOf(e);
		} finally {
			importing = false;
		}
	}

	async function load() {
		const [i, t, c, ch, pe] = await Promise.allSettled([api.database(app, env), api.dbTables(app, env), api.appDatabase(app), api.dbChanges(app, env, undefined, CHANGES_PAGE), api.people()]);
		if (ch.status === 'fulfilled') {
			changes = ch.value;
			moreChanges = hasMoreChanges(ch.value.length, CHANGES_PAGE);
		}
		if (pe.status === 'fulfilled') people = pe.value;
		if (i.status === 'fulfilled') info = i.value;
		if (t.status === 'fulfilled') tables = t.value;
		if (c.status === 'fulfilled') appDb = c.value;
		const failed = i.status === 'rejected' ? i.reason : null;
		error = failed ? messageOf(failed) : '';
		loaded = true;
	}
	onMount(() => {
		login = savedLogin();
		load();
	});

	const postgres = $derived(info?.engine === 'postgres');
	const mine = $derived(appDb?.envs.find((e) => e.env === env));
	const copies = $derived<DbCopy[]>(mine?.copies ?? []);

	// Rows of one table
	let open = $state<DbTable | null>(null);
	let rows = $state<DbRows | null>(null);
	let offset = $state(0);
	let rowsBusy = $state(false);
	let rowsError = $state('');
	async function showRows(t: DbTable, at = 0) {
		open = t;
		offset = at;
		rowsBusy = true;
		rowsError = '';
		try {
			rows = await api.dbRows(app, env, t, at, PAGE_ROWS);
		} catch (e) {
			rows = null;
			rowsError = messageOf(e);
		} finally {
			rowsBusy = false;
		}
	}

	// SQL box
	let sql = $state('');
	let allowChanges = $state(false);
	let result = $state<DbResult | null>(null);
	let sqlError = $state('');
	let running = $state(false);
	let confirmingRun = $state(false);
	let typedEnv = $state('');
	let askTyped = $state(false);
	function run(e: Event) {
		e.preventDefault();
		if (!sql.trim() || running) return;
		if (!askTyped && needsConfirm(env, postgres && allowChanges)) confirmingRun = true;
		else send();
	}
	async function send() {
		confirmingRun = false;
		running = true;
		sqlError = '';
		result = null;
		try {
			result = await api.dbQuery(app, env, sql, postgres && allowChanges, askTyped ? typedEnv.trim() : undefined);
			askTyped = false;
			typedEnv = '';
			if (postgres && allowChanges) await refreshChanges();
		} catch (e) {
			askTyped = e instanceof ApiError && e.status === 409 && e.needsConfirm;
			sqlError = messageOf(e);
		} finally {
			running = false;
		}
	}

	async function refreshChanges() {
		try {
			changes = await api.dbChanges(app, env, undefined, CHANGES_PAGE);
			moreChanges = hasMoreChanges(changes.length, CHANGES_PAGE);
		} catch {
			// The list stays as it was.
		}
	}

	// Copies
	let note = $state('');
	let copyError = $state('');
	let copyBusy = $state(false);
	let restoring = $state<DbCopy | null>(null);
	let copyingProd = $state(false);
	async function act(fn: () => Promise<void>) {
		copyBusy = true;
		copyError = '';
		note = '';
		try {
			await fn();
			note = 'Started.';
			restoring = null;
			copyingProd = false;
			await load();
		} catch (e) {
			copyError = messageOf(e);
		} finally {
			copyBusy = false;
		}
	}
	const copyNow = () => act(() => api.makeDbCopy(app, env));
	const restore = () => restoring && act(() => api.restoreDbCopy(app, env, restoring!.name));
	const copyProd = () => act(() => api.copyProdToDev(app));
	const closeRestore = () => ((restoring = null), (copyError = ''));
	const closeProd = () => ((copyingProd = false), (copyError = ''));

	// Connect from the Mac
	let login = $state('');
	let loginError = $state('');
	let tunnelCopied = $state(false);
	let password = $state('');
	let passwordError = $state('');
	let showing = $state(false);
	const tunnel = $derived(mine?.port ? tunnelCommand(mine.port, login, location.hostname) : '');
	async function copyTunnel() {
		loginError = loginProblem(login);
		if (loginError || !tunnel) return;
		saveLogin(login);
		tunnelCopied = await copyText(tunnel);
		setTimeout(() => (tunnelCopied = false), 2000);
	}
	async function showAddress() {
		passwordError = '';
		showing = true;
		try {
			password = await api.revealSecret(app, env as EnvName, 'DATABASE_PASSWORD');
		} catch (e) {
			passwordError = e instanceof ApiError && e.status === 403 ? 'Only people who can see secrets can see the password.' : messageOf(e);
		} finally {
			showing = false;
		}
	}
	const hideAddress = () => (password = '');
</script>

<svelte:head><title>{app} · {env} · Databases · Homelab</title></svelte:head>

{#snippet grid(columns: string[], data: (string | null)[][])}
	<div class="scroll">
		<table>
			<thead><tr>{#each columns as c}<th>{c}</th>{/each}</tr></thead>
			<tbody>
				{#each data as r}
					<tr>{#each r as cell}<td>{#if cell === null}<span class="muted">null</span>{:else}{cell}{/if}</td>{/each}</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/snippet}

<div class="page">
	<div class="page-head"><h1>{app} · {env}</h1><a class="btn small" href="/databases">All databases</a></div>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if info}
		<p class="muted">{info.engine} · {dbStatusWords(info.status)} · {bytes(info.size_bytes)} · {tableCount(info.tables)}</p>

		<section class="card" aria-labelledby="tables-h">
			<h2 id="tables-h">Tables</h2>
			{#if tables.length === 0}
				<p class="muted none">No tables yet.</p>
			{:else}
				<ul>
					{#each tables as t (t.schema + '.' + t.name)}
						<li>
							<button class="table-btn" type="button" aria-pressed={open === t} onclick={() => showRows(t)}>
								<strong>{t.name}</strong>
								<span class="muted small">{t.rows} {t.rows === 1 ? 'row' : 'rows'}</span>
							</button>
						</li>
					{/each}
				</ul>
			{/if}
		</section>

		{#if open}
			<section class="card" aria-labelledby="rows-h">
				<h2 id="rows-h">{open.name}</h2>
				{#if rowsError}<p class="notice bad" role="alert">{rowsError}</p>{/if}
				{#if rowsBusy}
					<p class="muted loading"><Spinner /> Loading…</p>
				{:else if rows}
					{#if rows.rows.length === 0}
						<p class="muted none">{offset > 0 ? 'No more rows.' : 'No rows.'}</p>
					{:else}
						{@render grid(rows.columns, rows.rows)}
					{/if}
					<div class="row-actions">
						<button class="btn small" type="button" disabled={offset === 0} onclick={() => showRows(open!, Math.max(0, offset - PAGE_ROWS))}>Previous</button>
						<span class="muted small">{rowsRange(offset, rows.rows.length)}</span>
						<button class="btn small" type="button" disabled={rows.rows.length < PAGE_ROWS} onclick={() => showRows(open!, offset + PAGE_ROWS)}>Next</button>
					</div>
				{/if}
			</section>
		{/if}

		<section class="card" aria-labelledby="sql-h">
			<h2 id="sql-h">SQL</h2>
			<form onsubmit={run}>
				<label class="sr-only" for="sql">SQL</label>
				<textarea id="sql" bind:value={sql} oninput={() => ((askTyped = false), (typedEnv = ''))} rows="5" spellcheck="false" autocapitalize="none" autocomplete="off" placeholder="select * from notes"></textarea>
				{#if postgres}
					<label class="check"><input type="checkbox" bind:checked={allowChanges} /> Allow changes</label>
				{/if}
				<p class="muted small hint">{writeHint(postgres && allowChanges)}</p>
				{#if env === 'prod' && postgres}<p class="muted small hint">{PROD_COPY_NOTE}</p>{/if}
				{#if askTyped}
					<label for="typed-env">{confirmLabel(env)}</label>
					<input id="typed-env" type="text" bind:value={typedEnv} autocomplete="off" autocapitalize="none" spellcheck="false" placeholder={env} />
				{/if}
				<div class="row-actions"><button class="btn primary" type="submit" disabled={running || !sql.trim() || (askTyped && !typedEnv.trim())}>{running ? 'Running…' : 'Run'}</button></div>
			</form>
			{#if sqlError}<p class="notice bad" role="alert">{sqlError}</p>{/if}
			{#if result}
				<p class="muted small" role="status">{result.command} · {result.ms} ms{#if result.columns.length} · {result.row_count} {result.row_count === 1 ? 'row' : 'rows'}{/if}</p>
				{#if result.truncated}<p class="muted small">Showing the first 500 rows.</p>{/if}
				{#if result.columns.length}{@render grid(result.columns, result.rows)}{/if}
			{/if}
		</section>

		<section class="card" aria-labelledby="copies-h">
			<h2 id="copies-h">Copies</h2>
			{#if note}<p class="muted small" role="status">{note}</p>{/if}
			{#if copyError && !restoring && !copyingProd}<p class="notice bad" role="alert">{copyError}</p>{/if}
			{#if copies.length === 0}
				<p class="muted none">No copies yet.</p>
			{:else}
				<ul>
					{#each copies as c (c.name)}
						<li class="copy">
							<div class="info">
								<strong>{copyKindWords(c.kind)}</strong>
								<p class="muted small">{bytes(c.size_bytes)} · <Time at={c.made} /></p>
							</div>
							<button class="btn small" type="button" disabled={copyBusy} onclick={() => (restoring = c)}>Restore<span class="sr-only"> {c.name}</span></button>
						</li>
					{/each}
				</ul>
			{/if}
			<div class="row-actions">
				<button class="btn primary" type="button" disabled={copyBusy} onclick={copyNow}>{copyBusy && !restoring && !copyingProd ? 'Copying…' : 'Copy now'}</button>
				{#if env === 'prod'}<button class="btn" type="button" disabled={copyBusy} onclick={() => (copyingProd = true)}>Copy prod to dev</button>{/if}
			</div>
		</section>

		<section class="card" aria-labelledby="import-h">
			<h2 id="import-h">Import</h2>
			<p class="muted small">Replace this database with a file.</p>
			{#if importNote}<p class="muted small" role="status">{importNote}</p>{/if}
			{#if importError && !importFile}<p class="notice bad" role="alert">{importError}</p>{/if}
			<label class="sr-only" for="import-file">Choose a database file</label>
			<input id="import-file" type="file" accept=".db,.sqlite,.sqlite3,.dump,.sql,*/*" bind:this={picker} onchange={picked} />
		</section>

		<section class="card" aria-labelledby="changes-h">
			<h2 id="changes-h">Changes</h2>
			{#if changes.length === 0}
				<p class="muted none">No changes yet.</p>
			{:else}
				<ul>
					{#each changes as c (c.id)}
						<li class="change">
							<p class="small"><strong class={c.ok ? '' : 'failed'}>{changeWords(c.ok, c.rows)}</strong> · {personName(people, c.person)} · <Time at={changeTime(c.at)} /></p>
							{#if !c.ok && c.error}<p class="notice bad small">{c.error}</p>{/if}
							<details>
								<summary class="small">SQL</summary>
								<pre>{c.sql}</pre>
							</details>
							{#if c.copy}
								{@const made = copies.find((x) => x.name === c.copy)}
								<p class="muted small">Copy made first: {c.copy}</p>
								{#if made}<div class="row-actions"><button class="btn small" type="button" disabled={copyBusy} onclick={() => (restoring = made)}>Restore<span class="sr-only"> {c.copy}</span></button></div>{/if}
							{/if}
						</li>
					{/each}
				</ul>
				{#if changesError}<p class="notice bad" role="alert">{changesError}</p>{/if}
				{#if moreChanges}<div class="row-actions"><button class="btn small" type="button" disabled={changesBusy} onclick={loadMoreChanges}>{changesBusy ? 'Loading…' : 'Load more'}</button></div>{/if}
			{/if}
		</section>

		{#if postgres && mine?.port}
			<section class="card" aria-labelledby="mac-h">
				<h2 id="mac-h">Connect from your Mac</h2>
				<label for="login">Your login on this device</label>
				<input id="login" type="text" bind:value={login} autocomplete="off" autocapitalize="none" spellcheck="false" placeholder="alex" />
				{#if loginError}<p class="notice bad" role="alert">{loginError}</p>{/if}
				<p class="line"><code>{tunnel}</code></p>
				<div class="row-actions"><button class="btn small" type="button" onclick={copyTunnel}>{tunnelCopied ? 'Copied' : 'Copy'}<span class="sr-only"> the tunnel command</span></button></div>
				<h3>Address</h3>
				{#if password}
					<RevealedValue value={dbAddress(password)} label="the address" onhide={hideAddress} />
				{:else}
					<div class="row-actions"><button class="btn small" type="button" disabled={showing} onclick={showAddress}>{showing ? 'Getting it…' : 'Show address'}</button></div>
				{/if}
				{#if passwordError}<p class="notice bad" role="alert">{passwordError}</p>{/if}
				<p class="muted small hint">Run the first line in Terminal and keep it open. Paste the address into TablePlus or psql.</p>
			</section>
		{/if}
	{/if}

	{#if restoring}
		<Sheet title="Put this copy back?" onclose={closeRestore}>
			{#snippet children()}
				<p>Put this copy back? The app stops for a moment. A copy of the current data is made first.</p>
				{#if copyError}<p class="notice bad" role="alert">{copyError}</p>{/if}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={closeRestore}>Cancel</button>
				<button class="btn danger" type="button" disabled={copyBusy} onclick={restore}>{copyBusy ? 'Starting…' : 'Restore'}</button>
			{/snippet}
		</Sheet>
	{/if}

	{#if importFile}
		<Sheet title="Replace this database?" onclose={closeImport}>
			{#snippet children()}
				<p>Replace this database with the file? A copy of the current data is made first.</p>
				<p class="muted small">{importFile?.name} · {bytes(importFile?.size ?? 0)}</p>
				{#if importing}<progress max="1" value={progress} aria-label="Upload progress"></progress> <span class="muted small" role="status">{percent(progress)}</span>{/if}
				{#if importError}<p class="notice bad" role="alert">{importError}</p>{/if}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" disabled={importing} onclick={closeImport}>Cancel</button>
				<button class="btn danger" type="button" disabled={importing} onclick={doImport}>{importing ? 'Sending…' : 'Import'}</button>
			{/snippet}
		</Sheet>
	{/if}

	{#if copyingProd}
		<Sheet title="Copy prod to dev?" onclose={closeProd}>
			{#snippet children()}
				<p>Replace dev's data with a copy of prod? Dev's current data is copied first.</p>
				{#if copyError}<p class="notice bad" role="alert">{copyError}</p>{/if}
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={closeProd}>Cancel</button>
				<button class="btn danger" type="button" disabled={copyBusy} onclick={copyProd}>{copyBusy ? 'Starting…' : 'Copy prod to dev'}</button>
			{/snippet}
		</Sheet>
	{/if}

	{#if confirmingRun}
		<Sheet title="Change prod's data?" onclose={() => (confirmingRun = false)}>
			{#snippet children()}
				<p>This changes prod's data. Run it?</p>
			{/snippet}
			{#snippet footer()}
				<button class="btn" type="button" onclick={() => (confirmingRun = false)}>Cancel</button>
				<button class="btn danger" type="button" onclick={send}>Run it</button>
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
	h3 {
		margin: 0.9rem 0 0.3rem;
		font-size: 1rem;
	}
	.none {
		margin: 0.25rem 0;
	}
	.hint {
		margin: 0.6rem 0 0;
	}
	.row-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.6rem;
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		padding: 0.5rem 0;
		border-top: 1px solid var(--border);
	}
	li:first-child {
		border-top: 0;
	}
	.table-btn {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		width: 100%;
		min-height: 44px;
		padding: 0;
		border: 0;
		background: none;
		font: inherit;
		color: var(--text);
		text-align: left;
		cursor: pointer;
		overflow-wrap: anywhere;
	}
	.copy {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.info {
		flex: 1;
		min-width: 0;
	}
	.info p {
		margin: 0.15rem 0 0;
	}
	textarea {
		display: block;
		width: 100%;
		min-height: 7rem;
		padding: 0.6rem 0.7rem;
		font: inherit;
		font-family: var(--mono);
		border-radius: 10px;
		border: 1px solid var(--border-strong);
		background: var(--bg);
		color: var(--text);
		resize: vertical;
	}
	.check {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
	}
	.change p {
		margin: 0.15rem 0;
	}
	.failed {
		color: var(--bad, inherit);
	}
	summary {
		cursor: pointer;
		min-height: 44px;
		display: flex;
		align-items: center;
	}
	pre {
		margin: 0.25rem 0;
		padding: 0.5rem 0.6rem;
		font-family: var(--mono);
		font-size: 0.85rem;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--bg);
	}
	progress {
		width: 100%;
	}
	.line {
		margin: 0.8rem 0 0;
		overflow-wrap: anywhere;
	}
	.scroll {
		overflow-x: auto;
		margin-top: 0.5rem;
	}
	table {
		border-collapse: collapse;
		font-size: 0.875rem;
	}
	th,
	td {
		padding: 0.35rem 0.6rem;
		border-top: 1px solid var(--border);
		text-align: left;
		white-space: nowrap;
		max-width: 20rem;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	th {
		border-top: 0;
		font-weight: 600;
	}
</style>
