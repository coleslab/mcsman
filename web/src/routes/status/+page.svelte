<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type PublicServer } from '#lib/api.ts';
	import CopyField from '#lib/CopyField.svelte';
	import StatusPill from '#lib/StatusPill.svelte';

	let servers = $state<PublicServer[] | null>(null);
	let error = $state('');
	let updated = $state<Date | null>(null);

	async function load() {
		try {
			servers = (await api.status()).servers;
			updated = new Date();
			error = '';
		} catch {
			error = 'Could not load server status.';
		}
	}

	onMount(() => {
		load();
		// Anonymous visitors have no live socket; poll instead.
		const t = setInterval(() => document.visibilityState === 'visible' && load(), 15_000);
		return () => clearInterval(t);
	});
</script>

<svelte:head><title>Server status · mcsman</title></svelte:head>

<main class="container stack">
	<div class="row">
		<h1>Server status</h1>
		<span class="spacer"></span>
		{#if updated}<span class="muted small">Updated {updated.toLocaleTimeString()}</span>{/if}
	</div>

	{#if error}<div class="alert error">{error}</div>{/if}

	{#if servers === null && !error}
		<p class="muted">Loading…</p>
	{:else if servers?.length === 0}
		<div class="card empty">No servers yet.</div>
	{:else if servers}
		<ul class="list">
			{#each servers as s (s.name)}
				<li class="card stack">
					<div class="row">
						<h2>{s.name}</h2>
						<span class="spacer"></span>
						<StatusPill status={s.status} />
					</div>
					<div class="meta muted">
						<span>Minecraft {s.version}</span>
						{#if s.status === 'running'}
							<span>{s.player_count} {s.player_count === 1 ? 'player' : 'players'} online</span>
						{/if}
					</div>
					{#if s.connection_address}
						<CopyField value={s.connection_address} />
					{:else}
						<p class="muted small none">Ask an admin for the address.</p>
					{/if}
				</li>
			{/each}
		</ul>
	{/if}
</main>

<style>
	.list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 12px;
		grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
	}
	.meta {
		display: flex;
		gap: 12px;
		flex-wrap: wrap;
		font-size: 0.9rem;
	}
	.small {
		font-size: 0.8rem;
	}
	.none {
		margin: 0;
	}
</style>
