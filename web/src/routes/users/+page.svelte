<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, type CreatedInvite, type Invite, type Server, type User } from '#lib/api.ts';
	import { ago, dateTime } from '#lib/format.ts';
	import { session } from '#lib/session.svelte.ts';
	import CopyField from '#lib/CopyField.svelte';

	let users = $state<User[] | null>(null);
	let invites = $state<Invite[]>([]);
	let servers = $state<Server[]>([]);
	let error = $state('');
	let busy = $state('');

	let expires = $state('48h');
	let whitelistServer = $state('');
	let created = $state<CreatedInvite | null>(null);

	async function load() {
		try {
			[users, invites, servers] = await Promise.all([api.users(), api.invites(), api.servers()]);
		} catch (e) {
			error = (e as Error).message;
		}
	}

	$effect(() => {
		if (session.isAdmin) load();
		else goto('/status', { replaceState: true });
	});

	async function run(key: string, fn: () => Promise<unknown>) {
		busy = key;
		error = '';
		try {
			await fn();
			await load();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	function createInvite(e: SubmitEvent) {
		e.preventDefault();
		run('invite', async () => {
			created = await api.createInvite(expires, whitelistServer || undefined);
			// The daemon builds the URL from http.base_url; fall back to this origin.
			if (!/^https?:\/\//.test(created.url)) created.url = location.origin + '/join/' + created.token;
		});
	}

	function setRole(u: User, role: 'admin' | 'player') {
		const msg = role === 'admin' ? `Make ${u.username} an admin? Admins have full control.` : `Remove admin rights from ${u.username}?`;
		if (confirm(msg)) run(u.id, () => api.setRole(u.id, role));
	}

	function setMC(u: User) {
		const mc = prompt(
			`Minecraft username for ${u.username} (leave empty to clear). Their whitelist entries move with it.`,
			u.mc_username ?? ''
		);
		if (mc !== null) run(u.id, () => api.setUserMC(u.id, mc.trim()));
	}

	function remove(u: User) {
		if (confirm(`Remove ${u.username}? They will be signed out immediately.`)) run(u.id, () => api.deleteUser(u.id));
	}
</script>

<svelte:head><title>Users · mcsman</title></svelte:head>

<main class="container stack">
	<h1>Users</h1>
	{#if error}<div class="alert error">{error}</div>{/if}

	<section class="card stack">
		<h2>Invite a friend</h2>
		<p class="muted small">Invite links work once. Only the person you send it to can create an account with it.</p>
		<form class="invite" onsubmit={createInvite}>
			<label class="field">
				Expires in
				<select bind:value={expires}>
					<option value="1h">1 hour</option>
					<option value="24h">1 day</option>
					<option value="48h">2 days</option>
					<option value="7d">7 days</option>
					<option value="30d">30 days</option>
				</select>
			</label>
			<label class="field">
				Whitelist on
				<select bind:value={whitelistServer}>
					<option value="">No server (site only)</option>
					{#each servers as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
				</select>
			</label>
			<button class="primary" type="submit" disabled={busy === 'invite'}>Create link</button>
		</form>
		{#if created}
			<div class="created stack">
				<span class="small">
					Send this link{created.whitelist_server_name ? ` (whitelists on ${created.whitelist_server_name})` : ''}. It expires
					{dateTime(created.expires_at)} and won't be shown again.
				</span>
				<CopyField value={created.url} />
			</div>
		{/if}

		{#if invites.length}
			<h3 class="sub">Pending invites</h3>
			<ul class="rows">
				{#each invites as i (i.id)}
					<li>
						<div class="grow">
							<span>Created {ago(i.created_at)}</span>
							<span class="muted small">
								expires {dateTime(i.expires_at)}{i.whitelist_server_name ? ` · whitelists on ${i.whitelist_server_name}` : ''}
							</span>
						</div>
						<button class="small danger" disabled={busy === i.id} onclick={() => run(i.id, () => api.revokeInvite(i.id))}>
							Revoke
						</button>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<section class="card stack">
		<h2>Accounts</h2>
		{#if users === null}
			<p class="muted">Loading…</p>
		{:else}
			<ul class="rows">
				{#each users as u (u.id)}
					{@const self = u.id === session.user?.id}
					<li>
						<div class="grow">
							<div class="row">
								<strong>{u.username}</strong>
								<span class="role {u.role}">{u.role}</span>
								{#if self}<span class="muted small">(you)</span>{/if}
							</div>
							<span class="muted small">
								{u.mc_username ? `Minecraft: ${u.mc_username} · ` : ''}{u.last_login_at
									? `last seen ${ago(u.last_login_at)}`
									: 'never logged in'}
							</span>
						</div>
						<div class="btns">
							<button class="small" disabled={busy === u.id} onclick={() => setMC(u)}>Minecraft name</button>
							{#if u.role === 'player'}
								<button class="small" disabled={busy === u.id} onclick={() => setRole(u, 'admin')}>Make admin</button>
							{:else}
								<button class="small" disabled={busy === u.id} onclick={() => setRole(u, 'player')}>Make player</button>
							{/if}
							{#if !self}
								<button class="small danger" disabled={busy === u.id} onclick={() => remove(u)}>Remove</button>
							{/if}
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	</section>
</main>

<style>
	.small {
		font-size: 0.85rem;
	}
	.sub {
		font-size: 0.95rem;
		margin-top: 8px;
	}
	p {
		margin: 0;
	}
	.invite {
		display: grid;
		grid-template-columns: 1fr 1fr auto;
		gap: 12px;
		align-items: end;
	}
	@media (max-width: 600px) {
		.invite {
			grid-template-columns: 1fr;
		}
	}
	.created {
		padding: 12px;
		border-radius: var(--radius-sm);
		background: var(--accent-soft);
		gap: 8px;
	}
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.rows li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
		padding: 10px 0;
		border-top: 1px solid var(--border);
	}
	.grow {
		flex: 1;
		min-width: 200px;
		display: flex;
		flex-direction: column;
	}
	.role {
		font-size: 0.72rem;
		padding: 1px 8px;
		border-radius: 999px;
		background: var(--surface-2);
		color: var(--text-2);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-weight: 600;
	}
	.role.admin {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.btns {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
</style>
