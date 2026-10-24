<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { messageOf } from '#lib/errors.ts';
	import { absoluteTime } from '#lib/format.ts';
	import { FILTER_LEVELS, RANGES, addCounts, chipFor, extraText, levelInfo, lineTime, mergeNewer, newestId, noCounts, sinceFor } from '#lib/logs.ts';
	import { startPolling } from '#lib/poll.ts';
	import type { LogCounts, LogLevel, LogLine, LogQuery } from '#lib/types.ts';
	import Spinner from '#lib/components/Spinner.svelte';

	const PAGE = 100;
	const params = page.url.searchParams;
	const wanted = (params.get('level') ?? '').split(',').filter((l): l is LogLevel => FILTER_LEVELS.some((x) => x.level === l));

	let q = $state(params.get('q') ?? '');
	let app = $state(params.get('app') ?? '');
	let env = $state(['dev', 'prod'].includes(params.get('env') ?? '') ? (params.get('env') as string) : '');
	let range = $state(RANGES.some((r) => r.id === params.get('range')) ? (params.get('range') as string) : '24h');
	let levels = $state<LogLevel[]>(wanted);
	let tail = $state(false);

	let lines = $state<LogLine[]>([]);
	let counts = $state<LogCounts>(noCounts());
	let next = $state<string | null>(null);
	let loaded = $state(false);
	let busy = $state(false);
	let error = $state('');
	let open = $state<Set<number>>(new Set());
	let round = 0; // a newer search makes an older answer stale

	// What the filters mean right now; the effect below searches again when this changes.
	const base = $derived<LogQuery>({ q, app, env, level: levels, since: sinceFor(range) });

	onMount(() => {
		if (!appsStore.loaded) appsStore.refresh();
	});

	async function load() {
		const mine = ++round;
		busy = true;
		try {
			const r = await api.searchLogs({ ...base, since: sinceFor(range), limit: PAGE });
			if (mine !== round) return;
			lines = r.lines; counts = r.counts; next = r.next; error = ''; open = new Set();
		} catch (e) {
			if (mine === round) error = messageOf(e);
		} finally {
			if (mine === round) { busy = false; loaded = true; }
		}
	}

	async function older() {
		if (!next || busy) return;
		const mine = round;
		busy = true;
		try {
			const r = await api.searchLogs({ ...base, since: sinceFor(range), before: Number(next), limit: PAGE });
			if (mine !== round) return;
			lines = mergeNewer(lines, r.lines); next = r.next;
		} catch (e) {
			error = messageOf(e);
		} finally {
			busy = false;
		}
	}

	async function newer() {
		const after = newestId(lines);
		if (after === undefined) return load();
		const mine = round;
		const r = await api.searchLogs({ ...base, since: undefined, after, limit: 500 });
		if (mine !== round || r.lines.length === 0) return;
		lines = mergeNewer(lines, r.lines);
		counts = addCounts(counts, r.counts);
	}

	// Searches again whenever a filter changes (typing waits a moment).
	$effect(() => {
		JSON.stringify(base);
		range;
		const t = setTimeout(load, 250);
		return () => clearTimeout(t);
	});

	// Live tail: asks for newer lines every 2 s. The poller pauses while the page is hidden and stops when the page is left or tail is switched off.
	$effect(() => {
		if (!tail) return;
		return startPolling(async () => {
			try { await newer(); error = ''; } catch (e) { error = messageOf(e); }
		}, () => 2000);
	});

	function toggleLevel(l: LogLevel) {
		levels = levels.includes(l) ? levels.filter((x) => x !== l) : [...levels, l];
	}
	function toggleRow(id: number) {
		const s = new Set(open);
		if (!s.delete(id)) s.add(id);
		open = s;
	}
	const appNames = $derived(['homelab', ...appsStore.list.map((a) => a.id)]);
	const none = $derived(loaded && !error && lines.length === 0);
</script>

<svelte:head><title>Logs · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head">
		<h1>Logs</h1>
		<label class="tail"><input type="checkbox" bind:checked={tail} /> Live</label>
	</div>

	<form class="filters" role="search" onsubmit={(e) => { e.preventDefault(); load(); }}>
		<label class="sr-only" for="log-q">Search the logs</label>
		<input id="log-q" type="text" inputmode="search" placeholder="Search the logs" autocomplete="off" bind:value={q} />
		<div class="row3">
			<div>
				<label for="log-app">App</label>
				<input id="log-app" type="text" list="log-apps" placeholder="Any app" autocomplete="off" bind:value={app} />
				<datalist id="log-apps">{#each appNames as n}<option value={n}></option>{/each}</datalist>
			</div>
			<div>
				<label for="log-env">Environment</label>
				<select id="log-env" bind:value={env}>
					<option value="">Any</option>
					<option value="dev">Dev</option>
					<option value="prod">Prod</option>
				</select>
			</div>
			<div>
				<label for="log-range">When</label>
				<select id="log-range" bind:value={range}>
					{#each RANGES as r}<option value={r.id}>{r.label}</option>{/each}
				</select>
			</div>
		</div>
	</form>

	<div class="levels" role="group" aria-label="Show only these levels">
		{#each FILTER_LEVELS as l (l.level)}
			{#if l.level !== 'unknown' || counts.unknown > 0 || levels.includes('unknown')}
				<button type="button" class="lv {l.colour}" aria-pressed={levels.includes(l.level)} aria-label="{counts[l.level]} {l.word.toLowerCase()}, tap to {levels.includes(l.level) ? 'stop filtering' : 'filter'}" onclick={() => toggleLevel(l.level)}>
					<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={l.icon} /></svg>
					<span aria-hidden="true">{l.tag}</span>
					<strong aria-hidden="true">{counts[l.level]}</strong>
				</button>
			{/if}
		{/each}
		{#if levels.length}<button type="button" class="btn small" onclick={() => (levels = [])}>Show all</button>{/if}
	</div>

	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{#if tail}<p class="muted small live" role="status"><Spinner /> Showing new lines as they come.</p>{/if}

	{#if !loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if none}
		<p class="muted">No log lines match. Try a longer time or fewer filters.</p>
	{:else}
		<ol class="lines" aria-label="Log lines, newest first">
			{#each lines as l (l.id)}
				{@const info = levelInfo(l.level)}
				{@const chip = chipFor(l)}
				{@const extra = extraText(l.extra)}
				<li class="line {info.colour} {l.level}">
					<button type="button" class="head" aria-expanded={open.has(l.id)} onclick={() => toggleRow(l.id)}>
						<span class="meta">
							<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={info.icon} /></svg>
							<span class="tag" aria-label={info.word}>{info.tag}</span>
							<time datetime={l.time} title={absoluteTime(l.time)}>{lineTime(l.time)}</time>
							<span class="chip src" class:homelab={chip.homelab}>{chip.text}</span>
						</span>
						<span class="msg" class:full={open.has(l.id)}>{l.msg}</span>
					</button>
					{#if open.has(l.id)}
						<dl class="more">
							<dt>Time</dt><dd>{absoluteTime(l.time)}</dd>
							<dt>Stream</dt><dd>{l.stream}</dd>
							{#if extra}<dt>Details</dt><dd><pre>{extra}</pre></dd>{/if}
						</dl>
					{/if}
				</li>
			{/each}
		</ol>
		{#if next}
			<button class="btn" type="button" onclick={older} disabled={busy}>{#if busy}<Spinner /> {/if}Older</button>
		{/if}
	{/if}
</div>

<style>
	.tail {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		margin: 0;
		min-height: 44px;
	}
	.tail input {
		width: 1.25rem;
		height: 1.25rem;
	}
	.filters label:not(.sr-only) {
		font-size: 0.8rem;
		margin: 0.5rem 0 0.2rem;
		color: var(--muted);
	}
	.row3 {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
		gap: 0 0.6rem;
	}
	.filters input[type='text'] {
		width: 100%;
	}
	.loading,
	.live {
		margin: 0;
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.levels {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
	}
	.lv {
		--c: var(--muted);
		font: inherit;
		font-size: 0.85rem;
		font-weight: 600;
		min-height: 44px;
		padding: 0.25rem 0.7rem;
		border-radius: 999px;
		border: 1px solid var(--c);
		background: var(--surface);
		color: var(--c);
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		cursor: pointer;
	}
	.lv[aria-pressed='true'] {
		background: var(--c);
		color: var(--bg);
	}
	.grey { --c: var(--muted); --bar: var(--border-strong); }
	.blue { --c: #3a6fd8; --bar: #6f95e6; }
	.green { --c: var(--ok); --bar: var(--ok); }
	.yellow { --c: var(--warn); --bar: #d99a1c; }
	.red { --c: var(--bad); --bar: var(--bad); }
	.purple { --c: #8a3fc7; --bar: #8a3fc7; }
	@media (prefers-color-scheme: dark) {
		.blue { --c: #8db0ff; --bar: #5c85e0; }
		.purple { --c: #c79bff; --bar: #a56fe0; }
	}
	.lines {
		list-style: none;
		margin: 0;
		padding: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		overflow: hidden;
		background: var(--surface);
	}
	.line {
		border-top: 1px solid var(--border);
		border-left: 5px solid var(--bar);
	}
	.line:first-child {
		border-top: 0;
	}
	.line.debug {
		opacity: 0.7;
	}
	.line.error {
		background: var(--bad-bg);
	}
	.line.fatal .msg,
	.line.fatal .tag {
		font-weight: 800;
	}
	.head {
		all: unset;
		box-sizing: border-box;
		display: block;
		width: 100%;
		padding: 0.5rem 0.7rem;
		cursor: pointer;
	}
	.head:focus-visible {
		outline: 3px solid var(--accent);
		outline-offset: -3px;
	}
	.meta {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.2rem 0.5rem;
		font-size: 0.8rem;
		color: var(--c);
	}
	.tag {
		font-family: var(--mono);
		font-weight: 700;
	}
	time {
		color: var(--muted);
		font-family: var(--mono);
	}
	.src {
		color: var(--text);
	}
	.src.homelab {
		background: var(--accent-soft);
		color: var(--accent);
		border: 1px dashed var(--accent);
		font-style: italic;
	}
	.msg {
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
		overflow-wrap: anywhere;
		font-family: var(--mono);
		font-size: 0.85rem;
		margin-top: 0.15rem;
		color: var(--text);
	}
	.msg.full {
		display: block;
		white-space: pre-wrap;
	}
	.more {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.2rem 0.8rem;
		margin: 0;
		padding: 0 0.7rem 0.6rem;
		font-size: 0.8rem;
	}
	.more dt {
		color: var(--muted);
	}
	.more dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	pre {
		margin: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
