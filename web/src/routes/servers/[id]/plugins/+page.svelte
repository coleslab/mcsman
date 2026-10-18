<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Plugin, type PluginSearchResult, type PluginUpdate } from '#lib/api.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';

	const ctx = useServer();

	let plugins = $state<Plugin[] | null>(null);
	let updates = $state<Record<string, PluginUpdate>>({});
	let error = $state('');
	let busy = $state('');
	let changed = $state(false);

	let query = $state('');
	let results = $state<PluginSearchResult[] | null>(null);
	let searching = $state(false);
	let searchNote = $state('');

	const installedKeys = $derived(new Set((plugins ?? []).map((p) => `${p.source}:${p.source_id}`)));

	async function load() {
		try {
			plugins = await api.plugins(ctx.id);
			const ups = await api.pluginUpdates(ctx.id).catch(() => []);
			updates = Object.fromEntries(ups.map((u) => [u.plugin_id, u]));
		} catch (e) {
			error = (e as Error).message;
		}
	}

	onMount(load);

	async function run(key: string, fn: () => Promise<unknown>) {
		busy = key;
		error = '';
		try {
			await fn();
			changed = true;
			await load();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	async function search(e: SubmitEvent) {
		e.preventDefault();
		if (!query.trim()) return;
		searching = true;
		searchNote = '';
		try {
			const r = await api.searchPlugins(query.trim(), ctx.server?.mc_version ?? '');
			results = r.results;
			const down = Object.keys(r.errors);
			if (down.length) searchNote = `${down.join(' and ')} could not be reached.`;
		} catch (err) {
			searchNote = (err as Error).message;
		} finally {
			searching = false;
		}
	}

	function remove(p: Plugin) {
		if (confirm(`Remove ${p.name}? Its config folder is kept.`)) run(p.id, () => api.removePlugin(ctx.id, p.id));
	}

	function fmt(n: number) {
		return n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(0)}k` : String(n);
	}
</script>

<svelte:head><title>Plugins · {ctx.server?.name ?? ''} · mcsman</title></svelte:head>

<section class="stack">
	{#if changed && ctx.status === 'running'}
		<div class="alert warn row">
			<span>Plugin changes take effect after a restart.</span>
		</div>
	{/if}
	{#if error}<div class="alert error">{error}</div>{/if}

	<div class="card stack">
		<h2>Installed</h2>
		{#if plugins === null}
			<p class="muted">Loading…</p>
		{:else if plugins.length === 0}
			<p class="muted">No plugins installed. Search below to add some.</p>
		{:else}
			<ul class="rows">
				{#each plugins as p (p.id)}
					{@const up = updates[p.id]}
					<li>
						<div class="grow">
							<div class="row">
								<strong>{p.name}</strong>
								<span class="badge {p.source}">{p.source}</span>
							</div>
							<span class="muted small mono">{p.version} · {p.filename}</span>
						</div>
						<div class="btns">
							{#if up}
								<button class="small primary" disabled={!!busy} onclick={() => run(p.id, () => api.updatePlugin(ctx.id, p.id))}>
									{busy === p.id ? 'Updating…' : `Update to ${up.latest}`}
								</button>
							{/if}
							<button class="small danger" disabled={!!busy} onclick={() => remove(p)}>Remove</button>
						</div>
					</li>
				{/each}
			</ul>
		{/if}
	</div>

	<div class="card stack">
		<h2>Add plugins</h2>
		<form class="search" onsubmit={search}>
			<input bind:value={query} placeholder="Search Modrinth and Hangar…" autocapitalize="none" />
			<button type="submit" class="primary" disabled={searching}>{searching ? 'Searching…' : 'Search'}</button>
		</form>
		<p class="muted small">Showing plugins that support Minecraft {ctx.server?.mc_version}.</p>
		{#if searchNote}<div class="alert warn">{searchNote}</div>{/if}
		{#if results?.length === 0}
			<p class="muted">No results.</p>
		{:else if results}
			<ul class="rows">
				{#each results as r (r.source + r.id)}
					{@const installed = installedKeys.has(`${r.source}:${r.id}`)}
					<li>
						{#if r.icon_url}
							<img src={r.icon_url} alt="" width="40" height="40" loading="lazy" referrerpolicy="no-referrer" />
						{:else}
							<div class="icon-placeholder"></div>
						{/if}
						<div class="grow">
							<div class="row">
								<a href={r.page_url} target="_blank" rel="noreferrer"><strong>{r.name}</strong></a>
								<span class="badge {r.source}">{r.source}</span>
							</div>
							<span class="muted small desc">{r.description}</span>
							<span class="muted small">by {r.author} · {fmt(r.downloads)} downloads</span>
						</div>
						<button
							class="small"
							disabled={installed || !!busy}
							onclick={() => run(`i:${r.id}`, () => api.installPlugin(ctx.id, r.source, r.id))}
						>
							{installed ? 'Installed' : busy === `i:${r.id}` ? 'Installing…' : 'Install'}
						</button>
					</li>
				{/each}
			</ul>
		{/if}
	</div>
</section>

<style>
	.small {
		font-size: 0.82rem;
	}
	p {
		margin: 0;
	}
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.rows li {
		display: flex;
		align-items: center;
		gap: 12px;
		padding: 10px 0;
		border-top: 1px solid var(--border);
	}
	.grow {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
	}
	.desc {
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	img,
	.icon-placeholder {
		width: 40px;
		height: 40px;
		border-radius: 8px;
		flex-shrink: 0;
		background: var(--surface-2);
		object-fit: cover;
	}
	.btns {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
		justify-content: flex-end;
	}
	.search {
		display: flex;
		gap: 8px;
	}
	.badge {
		font-size: 0.7rem;
		padding: 1px 7px;
		border-radius: 999px;
		background: var(--surface-2);
		color: var(--text-2);
		font-weight: 600;
	}
	.badge.modrinth {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.badge.hangar {
		background: color-mix(in srgb, var(--info) 18%, transparent);
		color: var(--info);
	}
	a {
		text-decoration: none;
	}
	a:hover {
		text-decoration: underline;
	}
</style>
