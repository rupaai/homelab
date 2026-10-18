<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import { bytes, relativeTime, uptime } from '#lib/format.ts';
	import type { DeviceView, ResourcesView } from '#lib/types.ts';
	import Spinner from '#lib/components/Spinner.svelte';

	let view = $state<ResourcesView | null>(null);
	let error = $state('');
	let busy = $state(false);
	let device = $state<DeviceView | null>(null);
	let updating = $state(false);
	let updateMsg = $state('');

	async function load() {
		busy = true;
		try {
			const [r, d] = await Promise.all([api.resources(), api.device()]);
			view = r; device = d; error = '';
		}
		catch (e) { error = messageOf(e); }
		finally { busy = false; }
	}

	async function update() {
		const to = device?.update?.latest ?? 'the newest version';
		if (!confirm(`Update Homelab to ${to}? It restarts, so the companion is away for about a minute. If the new version doesn't stay up, Homelab goes back on its own.`)) return;
		updating = true; updateMsg = '';
		try {
			const v = await api.updateHomelab();
			updateMsg = `Updating to ${v}. Homelab is restarting; this page comes back in about a minute.`;
			setTimeout(() => { updating = false; load(); }, 60_000);
		} catch (e) { updateMsg = messageOf(e); updating = false; }
	}

	const unknown = (n: number | undefined) => n === undefined || n < 0;
	onMount(load);

	const pct = (used: number, total: number) => (total > 0 ? Math.round((used / total) * 100) : 0);
</script>

<svelte:head><title>Resources · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head">
		<div><h1>Resources</h1><p class="muted">What this computer and each app use right now, and how the device is doing.</p></div>
		<button class="btn small" type="button" disabled={busy} onclick={load}>{busy ? 'Checking…' : 'Refresh'}</button>
	</div>
	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{#if !view && !error}<p class="muted loading"><Spinner /> Checking…</p>{/if}
	{#if view}
		{#each view.problems ?? [] as p}<p class="notice">{p}</p>{/each}
		{#if view.device}
			{@const d = view.device}
			<section class="card" aria-labelledby="device">
				<h2 id="device">This computer</h2>
				<div class="meter"><span>Processor</span><strong>{Math.round(d.cpuPercent)}%</strong><progress max="100" value={d.cpuPercent} aria-label="Processor use"></progress></div>
				<div class="meter"><span>Memory</span><strong>{bytes(d.memUsed)} of {bytes(d.memTotal)}</strong><progress max="100" value={pct(d.memUsed, d.memTotal)} aria-label="Memory use"></progress></div>
				<div class="meter" class:warn={pct(d.diskUsed, d.diskTotal) >= 85}><span>Disk</span><strong>{bytes(d.diskUsed)} of {bytes(d.diskTotal)} ({pct(d.diskUsed, d.diskTotal)}%)</strong><progress max="100" value={pct(d.diskUsed, d.diskTotal)} aria-label="Disk use"></progress></div>
				{#if pct(d.diskUsed, d.diskTotal) >= 85}<p class="notice">The disk is almost full. Homelab sends admins a notification above 85%.</p>{/if}
			</section>
		{/if}
		{#if device}
			{#each device.problems ?? [] as p}<p class="notice">{p}</p>{/each}
			{#if device.system}
				{@const s = device.system}
				<section class="card" aria-labelledby="sys">
					<h2 id="sys">Device{#if s.hostname}{' '}<span class="muted">({s.hostname})</span>{/if}</h2>
					<dl class="facts">
						{#if s.os}<dt>System</dt><dd>{s.os}</dd>{/if}
						<dt>Running for</dt><dd>{uptime(s.uptimeSeconds)}</dd>
						{#if s.temperatureC}<dt>Temperature</dt><dd class:bad={s.temperatureC >= 85}>{Math.round(s.temperatureC)} °C</dd>{/if}
						<dt>OS updates</dt>
						<dd>
							{#if unknown(s.updatesPending)}<span class="muted">Not known on this computer</span>
							{:else if s.updatesPending === 0}Up to date
							{:else}{s.updatesPending} waiting{#if s.securityPending > 0}, {s.securityPending} for security (installed on their own each day){/if}{/if}
						</dd>
					</dl>
					{#if s.rebootRequired}<p class="notice">An update needs a restart of the device. Run <code>sudo reboot</code> on it when it suits you; apps come back on their own.</p>{/if}
					{#each s.disks as d (d.mount)}
						<div class="meter" class:warn={pct(d.used, d.total) >= 85 || d.health === 'failing'}>
							<span>Disk {d.mount}{#if d.health === 'ok'} <span class="chip">healthy</span>{:else if d.health === 'failing'} <span class="chip bad">failing</span>{/if}</span>
							<strong>{bytes(d.used)} of {bytes(d.total)} ({pct(d.used, d.total)}%)</strong>
							<progress max="100" value={pct(d.used, d.total)} aria-label="Disk {d.mount} use"></progress>
						</div>
						{#if d.health === 'failing'}<p class="notice bad">The disk at {d.mount} reports it is failing. Copy anything important off it soon.</p>{/if}
					{/each}
				</section>
			{/if}
			{#if device.update}
				{@const u = device.update}
				<section class="card" aria-labelledby="hl">
					<h2 id="hl">Homelab</h2>
					<dl class="facts">
						<dt>Version</dt><dd>{u.current}</dd>
						{#if u.latest}<dt>Newest</dt><dd>{u.latest}{#if u.checkedAt}{' '}<span class="muted">(checked {relativeTime(u.checkedAt)})</span>{/if}</dd>{/if}
					</dl>
					{#if u.lastResult}<p class="muted">{u.lastResult}</p>{/if}
					{#if u.note}<p class="muted">{u.note}</p>{/if}
					{#if u.managed && u.available}
						<button class="btn" type="button" disabled={updating} onclick={update}>{updating ? 'Updating…' : `Update to ${u.latest}`}</button>
					{:else if u.managed}<p class="muted">Homelab is up to date.</p>{/if}
					{#if updateMsg}<p class="notice" role="status">{updateMsg}</p>{/if}
				</section>
			{/if}
		{/if}
		<section class="card" aria-labelledby="apps">
			<h2 id="apps">Apps</h2>
			{#if view.apps.length === 0}<p class="muted">No app is running.</p>{:else}
				<table>
					<thead><tr><th scope="col">App</th><th scope="col">Processor</th><th scope="col">Memory</th></tr></thead>
					<tbody>
						{#each view.apps as a (a.app + a.env)}
							<tr><td><a href="/apps/{a.app}">{a.name}</a>{#if a.env !== 'prod'} <span class="chip">{a.env}</span>{/if}</td><td>{a.cpuPercent.toFixed(1)}%</td><td>{bytes(a.memUsed)}</td></tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</section>
	{/if}
</div>

<style>
	.page-head p { margin: 0.35rem 0 0; }
	.loading { display: flex; align-items: center; gap: 0.5rem; }
	.card + .card, .notice + .card { margin-top: 1rem; }
	h2 { font-size: 1.05rem; margin: 0 0 0.6rem; }
	.meter { display: grid; grid-template-columns: 1fr auto; gap: 0.2rem 0.75rem; margin: 0 0 0.8rem; }
	.meter progress { grid-column: 1 / -1; width: 100%; height: 0.5rem; accent-color: var(--accent); }
	.meter.warn progress { accent-color: var(--bad-border); }
	table { width: 100%; border-collapse: collapse; }
	th, td { text-align: left; padding: 0.45rem 0.25rem; border-bottom: 1px solid var(--border); }
	th { font-size: 0.85rem; color: var(--muted); font-weight: 600; }
	td:not(:first-child), th:not(:first-child) { text-align: right; white-space: nowrap; }
	.facts { display: grid; grid-template-columns: auto 1fr; gap: 0.35rem 1rem; margin: 0 0 0.9rem; }
	.facts dt { color: var(--muted); }
	.facts dd { margin: 0; }
	.facts dd.bad { color: var(--bad-border); font-weight: 600; }
	.chip.bad { border-color: var(--bad-border); color: var(--bad-border); }
	section .btn { margin-top: 0.25rem; }
	section .notice { margin: 0 0 0.8rem; }
</style>
