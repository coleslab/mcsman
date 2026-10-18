<script lang="ts">
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';
	import { provideServer } from '#lib/serverContext.svelte.ts';
	import StatusPill from '#lib/StatusPill.svelte';

	let { children } = $props();

	// Navigation between servers always passes through /servers, which
	// unmounts this layout, so the id is fixed for its lifetime.
	const ctx = provideServer(page.params.id ?? '');
	ctx.refresh();

	let acting = $state('');
	let actionError = $state('');

	const base = $derived(`/servers/${ctx.id}`);
	const tabs = $derived([
		{ href: base, label: 'Overview' },
		{ href: `${base}/console`, label: 'Console' },
		{ href: `${base}/plugins`, label: 'Plugins' },
		{ href: `${base}/backups`, label: 'Backups' },
		{ href: `${base}/worlds`, label: 'World' },
		{ href: `${base}/version`, label: 'Version' }
	]);

	async function act(name: string, fn: () => Promise<unknown>) {
		acting = name;
		actionError = '';
		try {
			await fn();
			await ctx.refresh();
		} catch (e) {
			actionError = (e as Error).message;
		} finally {
			acting = '';
		}
	}

	function stop() {
		const players = ctx.metrics?.player_count ?? 0;
		const delay = players > 0 ? 30 : 0;
		if (players > 0 && !confirm(`${players} player(s) online. Stop with a 30-second countdown?`)) return;
		act('stop', () => api.stop(ctx.id, delay));
	}

	function restart() {
		const players = ctx.metrics?.player_count ?? 0;
		const delay = players > 0 ? 30 : 0;
		if (players > 0 && !confirm(`${players} player(s) online. Restart with a 30-second countdown?`)) return;
		act('restart', () => api.restart(ctx.id, delay));
	}
</script>

<main class="container stack">
	<a href="/servers" class="muted back">← Servers</a>

	{#if ctx.error && !ctx.server}
		<div class="alert error">{ctx.error}</div>
	{:else if ctx.server && ctx.status}
		{@const status = ctx.status}
		<div class="head">
			<div class="title">
				<h1>{ctx.server.name}</h1>
				<StatusPill {status} />
			</div>
			<div class="actions">
				{#if status === 'stopped' || status === 'crashed'}
					<button class="primary" disabled={!!acting || !!ctx.server.busy} onclick={() => act('start', () => api.start(ctx.id))}>
						{acting === 'start' ? 'Starting…' : 'Start'}
					</button>
				{:else}
					<button disabled={!!acting || status !== 'running'} onclick={restart}>
						{acting === 'restart' ? 'Restarting…' : 'Restart'}
					</button>
					<button class="danger" disabled={!!acting || status === 'stopping'} onclick={stop}>
						{acting === 'stop' || status === 'stopping' ? 'Stopping…' : 'Stop'}
					</button>
				{/if}
			</div>
		</div>
		{#if actionError}<div class="alert error">{actionError}</div>{/if}
		{#if acting === 'start'}
			<p class="muted hint">First start may download a Java runtime; this can take a minute.</p>
		{/if}

		<nav class="tabs">
			{#each tabs as t (t.href)}
				<a href={t.href} class:active={page.url.pathname === t.href}>{t.label}</a>
			{/each}
		</nav>

		{@render children()}
	{:else}
		<p class="muted">Loading…</p>
	{/if}
</main>

<style>
	.back {
		font-size: 0.875rem;
		text-decoration: none;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: 12px;
		align-items: center;
		justify-content: space-between;
	}
	.title {
		display: flex;
		align-items: center;
		gap: 10px;
	}
	.actions {
		display: flex;
		gap: 8px;
	}
	.hint {
		margin: 0;
		font-size: 0.875rem;
	}
	.tabs {
		display: flex;
		gap: 4px;
		border-bottom: 1px solid var(--border);
		overflow-x: auto;
	}
	.tabs a {
		padding: 10px 14px;
		text-decoration: none;
		color: var(--text-2);
		font-weight: 550;
		border-bottom: 2px solid transparent;
		margin-bottom: -1px;
		white-space: nowrap;
	}
	.tabs a.active {
		color: var(--text);
		border-bottom-color: var(--accent);
	}
	@media (max-width: 520px) {
		.tabs {
			scrollbar-width: none;
			mask-image: linear-gradient(to right, #000 85%, transparent);
		}
		.tabs a {
			padding: 10px 8px;
			font-size: 0.875rem;
		}
		.actions {
			width: 100%;
		}
		.actions button {
			flex: 1;
		}
	}
</style>
