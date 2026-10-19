<script lang="ts">
	import { api, type CreatedInvite, type Member, type User } from '#lib/api.ts';
	import { ago, dateTime } from '#lib/format.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';
	import CopyField from '#lib/CopyField.svelte';

	const ctx = useServer();

	let members = $state<Member[] | null>(null);
	let users = $state<User[]>([]);
	let error = $state('');
	let notice = $state('');
	let busy = $state('');
	let pick = $state('');
	let invite = $state<CreatedInvite | null>(null);

	// Users who could be added: not members yet, sorted with addable ones first.
	const candidates = $derived(
		users
			.filter((u) => !members?.some((m) => m.user_id === u.id))
			.sort((a, b) => Number(!a.mc_username) - Number(!b.mc_username) || a.username.localeCompare(b.username))
	);
	const picked = $derived(users.find((u) => u.id === pick));

	async function load() {
		try {
			[members, users] = await Promise.all([api.members(ctx.id), api.users()]);
		} catch (e) {
			error = (e as Error).message;
		}
	}
	load();

	async function run(key: string, fn: () => Promise<unknown>) {
		busy = key;
		error = notice = '';
		try {
			await fn();
			await load();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	function add(e: SubmitEvent) {
		e.preventDefault();
		if (!picked) return;
		const u = picked;
		run('add', async () => {
			const r = await api.addMember(ctx.id, u.id);
			notice = r.queued
				? `Added ${u.username}. They'll be whitelisted as ${u.mc_username} when the server next starts.`
				: `Added ${u.username} and whitelisted ${u.mc_username}.`;
			pick = '';
		});
	}

	function remove(m: Member) {
		if (!confirm(`Remove ${m.username} from ${ctx.server?.name}? They'll be taken off the whitelist.`)) return;
		run(m.user_id, () => api.removeMember(ctx.id, m.user_id));
	}

	function createInvite() {
		run('invite', async () => {
			invite = await api.createInvite('7d', ctx.id);
			if (!/^https?:\/\//.test(invite.url)) invite.url = location.origin + '/join/' + invite.token;
		});
	}
</script>

<section class="card stack">
	<h2>Members</h2>
	<p class="muted small">
		Members can see this server's status, players, plugins and activity, and are on its whitelist. Removing a member takes
		them off the whitelist.
	</p>
	{#if error}<div class="alert error">{error}</div>{/if}
	{#if notice}<div class="alert ok">{notice}</div>{/if}

	<form class="add" onsubmit={add}>
		<label class="field">
			Add an existing user
			<select bind:value={pick}>
				<option value="">Choose a user…</option>
				{#each candidates as u (u.id)}
					<option value={u.id} disabled={!u.mc_username}>
						{u.username}{u.mc_username ? ` (${u.mc_username})` : ' — no Minecraft name'}
					</option>
				{/each}
			</select>
		</label>
		<button class="primary" type="submit" disabled={!picked?.mc_username || busy === 'add'}>Add</button>
	</form>
	{#if candidates.some((u) => !u.mc_username)}
		<p class="muted small">Users without a Minecraft name can't be whitelisted; set one on the Users page.</p>
	{/if}

	{#if members === null}
		<p class="muted">Loading…</p>
	{:else if members.length === 0}
		<p class="muted">No members yet.</p>
	{:else}
		<ul class="rows">
			{#each members as m (m.user_id)}
				<li>
					<div class="grow">
						<strong>{m.username}</strong>
						<span class="muted small">
							{m.mc_username ? `Minecraft: ${m.mc_username}` : 'no Minecraft name'} · member {ago(m.created_at)}
							{m.role === 'admin' ? ' · admin' : ''}
						</span>
					</div>
					<button class="small danger" disabled={busy === m.user_id} onclick={() => remove(m)}>Remove</button>
				</li>
			{/each}
		</ul>
	{/if}
</section>

<section class="card stack">
	<h2>Invite someone new</h2>
	<p class="muted small">
		A one-time link that's valid for 7 days. New people create an account; people who already have one log in and
		join with it.
	</p>
	<div><button disabled={busy === 'invite'} onclick={createInvite}>Create invite link</button></div>
	{#if invite}
		<span class="small">Expires {dateTime(invite.expires_at)}. It won't be shown again.</span>
		<CopyField value={invite.url} />
	{/if}
</section>

<style>
	p {
		margin: 0;
	}
	.small {
		font-size: 0.85rem;
	}
	.add {
		display: grid;
		grid-template-columns: 1fr auto;
		gap: 12px;
		align-items: end;
	}
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.rows li {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 0;
		border-top: 1px solid var(--border);
	}
	.grow {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.alert.ok {
		background: var(--accent-soft);
		color: var(--accent);
	}
</style>
