<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { clock } from '#lib/clock.svelte.ts';
	import { api } from '#lib/api.ts';
	import { appsStore } from '#lib/apps.svelte.ts';
	import { startPolling } from '#lib/poll.ts';
	import { autoStateLabel, countdown, promotionHelp, promotionLabel, relativeTime } from '#lib/format.ts';
	import type { ActivityEvent, EnvName, PromotionMode } from '#lib/types.ts';
	import DeploySheet from '#lib/components/DeploySheet.svelte';
	import EnvCard from '#lib/components/EnvCard.svelte';
	import EventList from '#lib/components/EventList.svelte';
	import PromotionSheet from '#lib/components/PromotionSheet.svelte';
	import PromoteSheet from '#lib/components/PromoteSheet.svelte';
	import RollbackSheet from '#lib/components/RollbackSheet.svelte';
	import RemoveAppSheet from '#lib/components/RemoveAppSheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	const id = $derived(page.params.id ?? '');
	const app = $derived(appsStore.get(id));

	let sheet = $state<null | { kind: 'promote' | 'deploy' } | { kind: 'rollback'; env: EnvName } | { kind: 'mode'; mode: PromotionMode } | { kind: 'remove' }>(null);
	const modes: PromotionMode[] = ['manual', 'auto-dev', 'auto'];
	let admin = $state(false);
	let events = $state<ActivityEvent[]>([]);
	let eventsLoaded = $state(false);

	async function loadEvents() {
		try {
			events = await api.activity({ app: id, limit: 10 });
		} catch {
			// the list just stays as it was
		} finally {
			eventsLoaded = true;
		}
	}

	onMount(() => {
		// Promotion, secrets and removing an app are for admins; builders don't see them.
		api.me().then((me) => (admin = me.roles.some((r) => r === 'owner' || r === 'admin')), () => {});
		const stop = startPolling(async () => {
			await appsStore.refresh();
			await loadEvents();
		}, () => appsStore.pollMs);
		return stop;
	});

	// After a change: close the sheet and refresh right away so progress shows up.
	function started() {
		sheet = null;
		appsStore.refresh();
		loadEvents();
	}

	async function removed() {
		sheet = null;
		await appsStore.refresh();
		goto('/');
	}
</script>

<svelte:head><title>{app?.name ?? 'App'} · Homelab</title></svelte:head>

<div class="page">
	<a class="back" href="/">← Projects</a>

	{#if !appsStore.loaded}
		<p class="muted loading"><Spinner /> Loading…</p>
	{:else if !app}
		<div class="card">
			<p><strong>There's no app called "{id}".</strong></p>
			<p class="muted">It may have been removed. <a href="/">Back to your projects</a>.</p>
		</div>
	{:else}
		<div class="head">
			<span class="icon" aria-hidden="true">{app.icon || app.name[0]?.toUpperCase()}</span>
			<div>
				<h1>{app.name}</h1>
				<p class="muted small"><a href={app.repoUrl} target="_blank" rel="noopener">{app.repo}<span class="sr-only"> (opens in a new tab)</span></a></p>
			</div>
		</div>

		{#if app.latestBuild?.status === 'failed'}
			<p class="notice bad">
				<strong>The latest build failed</strong>
				Build {app.latestBuild.short} won't be deployed. <a href={app.latestBuild.url} target="_blank" rel="noopener">See what went wrong<span class="sr-only"> (opens in a new tab)</span></a>
			</p>
		{:else if app.latestBuild?.status === 'running'}
			<p class="notice info"><Spinner /> A new build ({app.latestBuild.short}) is being made.</p>
		{/if}

		{#if app.versionProblem}
			<p class="notice"><strong>No versions for now</strong>{app.versionProblem} Deploys still work; they just have no version.</p>
		{/if}

		{#each app.envs as env (env.env)}
			<EnvCard
				{app}
				{env}
				onpromote={() => (sheet = { kind: 'promote' })}
				onrollback={() => (sheet = { kind: 'rollback', env: env.env })}
				ondeploy={() => (sheet = { kind: 'deploy' })}
				{admin}
			/>
		{/each}

		{#if admin}
			<section class="card" aria-labelledby="promotion">
				<h2 id="promotion">Promotion</h2>
				<fieldset>
					<legend class="sr-only">Promotion mode</legend>
					{#each modes as m}
						<label class="mode">
							<input
								type="radio"
								name="mode"
								value={m}
								checked={app.promotion === m}
								onchange={(e) => {
									e.currentTarget.checked = app.promotion === m;
									sheet = { kind: 'mode', mode: m };
								}}
							/>
							<span><strong>{promotionLabel(m)}</strong><span class="muted small">{promotionHelp(m, app.promoteAfter)}</span></span>
						</label>
					{/each}
				</fieldset>

				{#if app.autoPromote}
					<p class="auto" role="status">
						<strong>{autoStateLabel(app.autoPromote.state)}.</strong>
						{app.autoPromote.detail}
						{#if app.autoPromote.state === 'waiting' && app.autoPromote.at}
							<span class="muted">Promotes {countdown(app.autoPromote.at, clock.now)}.</span>
						{/if}
					</p>
				{/if}

				{#if app.held.length}
					<h3>Held commits</h3>
					<ul class="held">
						{#each app.held as h (h.commit)}
							<li><code>{h.short}</code> {h.reason} <span class="muted small">{relativeTime(h.at, clock.now)}</span></li>
						{/each}
					</ul>
					<p class="hint">Homelab won't promote these by itself. You can still promote them by hand.</p>
				{/if}
			</section>
		{/if}

		{#if admin || app.mirror}
		<section class="card" aria-labelledby="more">
			<h2 id="more">{admin ? 'Secrets and code' : 'Code'}</h2>
			{#if admin}<p><a href="/apps/{encodeURIComponent(app.id)}/secrets">Secrets for dev and prod</a></p>{/if}
			{#if admin}<p><a href="/databases">Databases</a></p>{/if}
			{#if app.mirror}
				{#if app.mirror.error}
					<p class="notice bad">The copy of the code on this computer isn't working. {app.mirror.error}{#if app.mirror.lastSync} It last worked <span>{relativeTime(app.mirror.lastSync, clock.now)}</span>.{/if}</p>
				{:else if app.mirror.lastSync}
					<p class="muted">Copy of the code on this computer, updated {relativeTime(app.mirror.lastSync, clock.now)}</p>
				{:else}
					<p class="muted">Copy of the code on this computer is being made.</p>
				{/if}
			{/if}
		</section>
		{/if}

		<section aria-labelledby="recent">
			<div class="page-head">
				<h2 id="recent">Recent activity</h2>
				<a class="small" href="/activity?app={encodeURIComponent(id)}">All activity</a>
			</div>
			<div class="card">
				{#if events.length}
					<EventList {events} showApp={false} />
				{:else if eventsLoaded}
					<p class="muted">Nothing has happened yet.</p>
				{:else}
					<p class="muted"><Spinner /> Loading…</p>
				{/if}
			</div>
		</section>

		{#if admin}
			<div class="danger-zone">
				<button class="btn" type="button" onclick={() => (sheet = { kind: 'remove' })}>Remove app…</button>
			</div>
		{/if}

		{#if sheet?.kind === 'mode'}
			<PromotionSheet {app} mode={sheet.mode} onclose={() => (sheet = null)} ondone={started} />
		{:else if sheet?.kind === 'remove'}
			<RemoveAppSheet {app} onclose={() => (sheet = null)} ondone={removed} />
		{:else if sheet?.kind === 'promote'}
			<PromoteSheet appId={app.id} onclose={() => (sheet = null)} ondone={started} />
		{:else if sheet?.kind === 'rollback'}
			<RollbackSheet appId={app.id} env={sheet.env} onclose={() => (sheet = null)} ondone={started} />
		{:else if sheet?.kind === 'deploy'}
			<DeploySheet {app} onclose={() => (sheet = null)} ondone={started} />
		{/if}
	{/if}
</div>

<style>
	.back {
		text-decoration: none;
		font-weight: 600;
		align-self: flex-start;
		padding: 0.25rem 0;
	}
	.loading {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.head p {
		margin: 0;
	}
	.icon {
		width: 3rem;
		height: 3rem;
		border-radius: 14px;
		background: var(--accent-soft);
		color: var(--accent);
		display: grid;
		place-items: center;
		font-size: 1.6rem;
		font-weight: 700;
	}
	.notice.info {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		border-color: var(--border-strong);
		background: var(--surface-2);
	}
	h2 {
		margin: 0;
	}
	fieldset {
		border: 0;
		padding: 0;
		margin: 0.5rem 0 0;
		display: grid;
		gap: 0.5rem;
	}
	.mode {
		display: flex;
		gap: 0.7rem;
		align-items: flex-start;
		margin: 0;
		padding: 0.6rem 0.7rem;
		border: 1px solid var(--border);
		border-radius: 10px;
		font-weight: normal;
		cursor: pointer;
	}
	.mode:has(input:checked) {
		border-color: var(--accent);
		background: var(--accent-soft);
	}
	.mode input {
		margin-top: 0.3rem;
		width: 1.1rem;
		height: 1.1rem;
		flex-shrink: 0;
	}
	.mode span {
		display: flex;
		flex-direction: column;
	}
	.auto {
		margin: 0.8rem 0 0;
	}
	.held {
		margin: 0;
		padding-left: 1.2rem;
	}
	#more + p {
		margin-top: 0.5rem;
	}
	.danger-zone {
		margin-top: 0.5rem;
	}
	section .page-head {
		margin-bottom: 0.5rem;
	}
</style>
