<script lang="ts">
	import { api, type Server } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';
	import StatusPill from '#lib/StatusPill.svelte';

	let servers = $state<Server[] | null>(null);
	let error = $state('');

	async function load() {
		try {
			servers = await api.servers();
			for (const s of servers) live.status[s.id] = s.status;
		} catch (e) {
			error = (e as Error).message;
		}
	}

	$effect(() => {
		if (session.user) load();
	});
</script>

<svelte:head><title>{session.isAdmin ? 'Servers' : 'My servers'} · mcsman</title></svelte:head>

<main class="container stack">
	{#if session.user}
		<div class="row">
			<h1>{session.isAdmin ? 'Servers' : 'My servers'}</h1>
			<span class="spacer"></span>
			{#if session.isAdmin}<a class="btn primary" href="/servers/new">+ New server</a>{/if}
		</div>

		{#if error}<div class="alert error">{error}</div>{/if}

		{#if servers === null && !error}
			<p class="muted">Loading…</p>
		{:else if servers?.length === 0 && session.isAdmin}
			<div class="card empty">
				<h2>No servers yet</h2>
				<p>Create your first Paper server to get started.</p>
				<a class="btn primary" href="/servers/new">Create a server</a>
			</div>
		{:else if servers?.length === 0}
			<div class="card empty">
				<h2>You haven't joined a server yet</h2>
				<p>Ask an admin to add you, or open an invite link they send you.</p>
				<a class="btn" href="/status">See server status</a>
			</div>
		{:else if servers}
			<ul class="grid">
				{#each servers as s (s.id)}
					{@const status = live.status[s.id] ?? s.status}
					{@const m = live.metrics[s.id] ?? s.metrics}
					<li>
						<a class="card server" href="/servers/{s.id}">
							<div class="row">
								<h2>{s.name}</h2>
								<span class="spacer"></span>
								<StatusPill {status} />
							</div>
							<div class="meta muted">
								<span>Paper {s.mc_version}</span>
								{#if s.port}<span>:{s.port}</span>{/if}
								{#if status === 'running' && m}
									<span>{m.player_count} online</span>
									<span>{m.tps.toFixed(1)} TPS</span>
								{/if}
							</div>
							{#if s.connection_address}
								<div class="addr mono">{s.connection_address}</div>
							{/if}
						</a>
					</li>
				{/each}
			</ul>
		{/if}
	{/if}
</main>

<style>
	.grid {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 12px;
		grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
	}
	.server {
		display: flex;
		flex-direction: column;
		gap: 8px;
		text-decoration: none;
		transition: border-color 0.12s;
	}
	.server:hover {
		border-color: var(--text-2);
	}
	.meta {
		display: flex;
		gap: 12px;
		flex-wrap: wrap;
		font-size: 0.875rem;
	}
	.addr {
		font-size: 0.85rem;
		color: var(--text-2);
	}
</style>
