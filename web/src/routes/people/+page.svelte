<script lang="ts">
	import { onMount } from 'svelte';
	import QRCode from 'qrcode';
	import { api } from '#lib/api.ts';
	import { messageOf } from '#lib/errors.ts';
	import type { AppView, HouseholdRole, Invitation, MeView, PersonInput, PersonView, RemovalStep } from '#lib/types.ts';
	import { canShare, copyText, shareLink } from '#lib/clipboard.ts';
	import Sheet from '#lib/components/Sheet.svelte';
	import Spinner from '#lib/components/Spinner.svelte';

	let people = $state<PersonView[]>([]);
	let apps = $state<AppView[]>([]);
	let me = $state<MeView | null>(null);
	let loaded = $state(false);
	let error = $state('');
	let saving = $state(false);
	let editor = $state(false);
	let editing = $state<PersonView | null>(null);
	let removing = $state<PersonView | null>(null);
	let pendingRemoval = $state<PersonView | null>(null);
	let name = $state('');
	let email = $state('');
	let roles = $state<HouseholdRole[]>([]);
	let grants = $state<Record<string, 'use'|'build'|''>>({});
	let invite = $state<Invitation | null>(null);
	let copied = $state(false);
	let invitedEmail = $state('');
	let invitationFor = $state('');
	let qrCanvas = $state<HTMLCanvasElement>();
	let steps = $state<RemovalStep[]>([]);

	async function load() {
		try { [people, apps, me] = await Promise.all([api.people(), api.apps(), api.me()]); error = ''; }
		catch (e) { error = messageOf(e); }
		finally { loaded = true; }
	}
	onMount(load);

	function openEditor(person: PersonView | null = null) {
		editing = person; name = person?.name ?? ''; email = person?.email ?? '';
		roles = person ? [...person.roles] : ['member'];
		grants = Object.fromEntries(apps.map((a) => [a.id, person?.grants[a.id] ?? '']));
		error = ''; editor = true;
	}
	const isOwner = $derived(!!me?.roles.includes('owner'));
	function roleLabel(role: string) { return role === 'secrets' ? 'can see secrets' : role; }
	function toggleRole(role: HouseholdRole, checked: boolean) {
		if (checked) { if (!roles.includes(role)) roles = [...roles, role]; }
		else roles = roles.filter((r) => r !== role);
	}
	function personInput(): PersonInput {
		return { name: name.trim(), email: email.trim(), roles, grants: Object.fromEntries(Object.entries(grants).filter((entry): entry is [string, 'use'|'build'] => entry[1] !== '')) };
	}
	async function save(e: SubmitEvent) {
		e.preventDefault(); saving = true; error = '';
		try {
			if (editing) await api.updatePerson(editing.id, personInput());
			else { const result = await api.invitePerson(personInput()); showInvite(result, name.trim(), email.trim()); }
			editor = false; await load();
		} catch (e) { error = messageOf(e); }
		finally { saving = false; }
	}
	async function resend(person: PersonView) {
		saving = true; error = '';
		try { const result = await api.resendInvite(person.id); showInvite(result, person.name, person.email ?? ''); await load(); }
		catch (e) { error = messageOf(e); }
		finally { saving = false; }
	}
	async function remove(person: PersonView) {
		if (!person) return; saving = true; error = '';
		try { steps = await api.removePerson(person.id); removing = null; pendingRemoval = null; await load(); }
		catch (e) { error = messageOf(e); }
		finally { saving = false; }
	}
	function showInvite(result: Invitation, who: string, to: string) { invite = result; invitationFor = who; invitedEmail = to; copied = false; }
	function closeInvite() { invite = null; }
	async function copy(text: string) {
		copied = await copyText(text);
		if (!copied) error = 'Your browser could not copy the link. Select and copy it instead.';
	}
	async function share(url: string) {
		try { await shareLink('Join Homelab', url); }
		catch { error = 'Your browser could not share the link. Copy it instead.'; }
	}
	$effect(() => {
		const url = invite?.url; const canvas = qrCanvas;
		if (url && canvas) void QRCode.toCanvas(canvas, url, { width: 200, margin: 1, errorCorrectionLevel: 'M' }).catch(() => { error = 'Homelab could not make the QR code. Copy the invite link instead.'; });
	});
	function labelStatus(status: string) { return status === 'signed_in' ? 'Signed in' : status === 'invited' ? 'Invited' : status; }
	function accessLabel(person: PersonView) { const entries = Object.entries(person.grants); return entries.length ? entries.map(([id, level]) => `${apps.find((a) => a.id === id)?.name ?? id} (${level})`).join(', ') : 'No app access'; }
</script>

<svelte:head><title>People · Homelab</title></svelte:head>

<div class="page">
	<div class="page-head"><div><h1>People</h1><p class="muted">Choose who can use Homelab and its apps.</p></div><button class="btn primary small" type="button" onclick={() => openEditor()}>Invite</button></div>
	{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
	{#if steps.length}<section class="card result" aria-live="polite"><h2>Removal checklist</h2>{#each steps as step}<p><strong>{step.done ? 'Done' : 'Needs attention'}:</strong> {step.name}{#if step.detail}<br /><span class="muted">{step.detail}</span>{/if}</p>{/each}<button class="btn small" type="button" onclick={() => (steps = [])}>Close</button></section>{/if}
	{#if !loaded}<p class="muted loading"><Spinner /> Loading people…</p>{:else if people.length === 0}<div class="card"><p>No one has been invited yet.</p></div>{/if}
	{#each people as person (person.id)}
		<article class="card person-card">
			<div class="person-main"><div class="avatar" aria-hidden="true">{person.name.slice(0,1).toUpperCase()}</div><div class="person-copy"><h2>{person.name}</h2><p class="muted">{person.email || 'No email'} · {labelStatus(person.status)}</p><p class="small">{person.roles.map(roleLabel).join(', ')} · {accessLabel(person)}</p>{#if person.lastSignIn}<p class="muted small">Last signed in {new Date(person.lastSignIn).toLocaleString()}</p>{/if}</div></div>
			<div class="person-actions"><button class="btn small" type="button" onclick={() => openEditor(person)}>Edit access<span class="sr-only"> for {person.name}</span></button>{#if person.status === 'invited'}<button class="btn small" type="button" disabled={saving} onclick={() => resend(person)}>Resend invite</button><button class="btn danger small" type="button" onclick={() => (pendingRemoval = person)}>Cancel invite</button>{:else if !person.roles.includes('owner')}<button class="btn danger small" type="button" onclick={() => (removing = person)}>Remove</button>{/if}</div>
		</article>
	{/each}
</div>

{#if editor}
	<Sheet title={editing ? `Edit ${editing.name}` : 'Invite someone'} onclose={() => (editor = false)}>
		{#snippet children()}
			<form id="person-form" onsubmit={save}>
				<label for="person-name">Name</label><input id="person-name" bind:value={name} autocomplete="name" required />
				<label for="person-email">Email <span class="muted">(optional)</span></label><input id="person-email" type="email" bind:value={email} autocomplete="email" />
					<fieldset><legend>Roles</legend>{#each ['owner','admin','builder','member'] as role}<label class="check"><input type="checkbox" checked={roles.includes(role)} disabled={role === 'owner' && (!editing || editing.roles.includes('owner'))} onchange={(e) => toggleRole(role, e.currentTarget.checked)} />{role}{#if role === 'owner'} <span class="muted">(cannot be assigned)</span>{/if}</label>{/each}
					<label class="check"><input type="checkbox" checked={roles.includes('secrets')} disabled={!isOwner || !!editing?.roles.includes('owner')} onchange={(e) => toggleRole('secrets', e.currentTarget.checked)} />Can see secrets</label>
					<p class="hint">{#if editing?.roles.includes('owner')}The owner can always see secrets.{:else if isOwner}They can see, change and restore secret values. Every look is written to Activity.{:else}Only the owner can change this.{/if}</p></fieldset>
				<fieldset><legend>App access</legend>{#if !apps.length}<p class="muted">Add an app before granting access.</p>{/if}{#each apps as app (app.id)}<label for="access-{app.id}">{app.name}</label><select id="access-{app.id}" bind:value={grants[app.id]}><option value="">No access</option><option value="use">Use prod</option><option value="build">Build and manage dev</option></select>{/each}</fieldset>
				{#if editing?.roles.includes('owner')}<p class="hint">The owner role stays on this account.</p>{/if}
				{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
			</form>
		{/snippet}
		{#snippet footer()}<button class="btn" type="button" onclick={() => (editor = false)}>Cancel</button><button class="btn primary" type="submit" form="person-form" disabled={saving || !name.trim() || !roles.length}>{saving ? 'Saving…' : editing ? 'Save access' : 'Create invite'}</button>{/snippet}
	</Sheet>
{/if}

{#if invite}
	<Sheet title="Invite {invitationFor}" onclose={closeInvite}>
		{#snippet children()}
			{@render inviteResult(invite!)}
		{/snippet}
		{#snippet footer()}<button class="btn primary" type="button" onclick={closeInvite}>Done</button>{/snippet}
	</Sheet>
{/if}

{#snippet inviteResult(inv: Invitation)}
	{#if inv.emailed}
		<p class="notice">We emailed the invite to {invitedEmail}.</p>
	{:else}
		<p class="notice bad" role="status">{inv.emailError || "Email isn't set up, so send the link yourself."}</p>
	{/if}
	<p>The link works for seven days. It can be used once.</p>
	<canvas bind:this={qrCanvas} aria-label="QR code for this invite">QR code for the invite link.</canvas>
	<label for="invite-link">Invite link</label><input id="invite-link" readonly value={inv.url} onclick={(e) => e.currentTarget.select()} />
	<div class="link-actions"><button class="btn small" type="button" onclick={() => copy(inv.url)}>{copied ? 'Copied' : 'Copy invite link'}</button>{#if canShare()}<button class="btn small" type="button" onclick={() => share(inv.url)}>Share</button>{/if}</div>
{/snippet}

{#if removing || pendingRemoval}
	{@const target = removing ?? pendingRemoval!}
	<Sheet title={pendingRemoval ? `Cancel ${target.name}’s invite?` : `Remove ${target.name}?`} onclose={() => ((removing = null), (pendingRemoval = null))}>
		{#snippet children()}
			<p>This will disable their Pocket ID sign-in, end their Homelab and app sessions, remove app access and revoke outstanding invite links. Pocket ID may keep sessions that were already open; the checklist will show that step.</p>
			{#if error}<p class="notice bad" role="alert">{error}</p>{/if}
		{/snippet}
		{#snippet footer()}<button class="btn" type="button" onclick={() => ((removing = null), (pendingRemoval = null))}>Keep person</button><button class="btn danger" type="button" disabled={saving} onclick={() => remove(target)}>{saving ? 'Removing…' : pendingRemoval ? 'Cancel invite' : 'Remove person'}</button>{/snippet}
	</Sheet>
{/if}

<style>
	.page-head{display:flex;align-items:center;justify-content:space-between;gap:1rem}.page-head p{margin:.35rem 0 0}
	.loading{display:flex;align-items:center;gap:.5rem}.person-card{display:grid;gap:.8rem}.person-main{display:flex;gap:.75rem;align-items:flex-start}.avatar{width:2.5rem;height:2.5rem;border-radius:50%;display:grid;place-items:center;background:var(--accent-soft);color:var(--accent);font-weight:700;flex:none}.person-copy{min-width:0}.person-copy h2,.person-copy p{margin:0 0 .2rem}.person-copy h2{font-size:1.05rem}.person-actions{display:flex;flex-wrap:wrap;gap:.4rem}.small{font-size:.88rem}.result h2{margin:.1rem 0 .6rem}.result p{margin:.4rem 0}
	form{display:grid;gap:.45rem}fieldset{border:1px solid var(--border);border-radius:10px;padding:.6rem .75rem;margin:.5rem 0}legend{font-weight:700;padding:0 .2rem}.check{display:flex;align-items:center;gap:.5rem;margin:.35rem 0}.check input{width:auto}select{width:100%;margin:0 0 .6rem}	.link-actions{display:flex;flex-wrap:wrap;gap:.4rem;margin-top:.5rem}canvas{display:block;margin:.5rem auto 1rem;background:white;padding:.5rem;border-radius:8px;max-width:100%}
</style>
