<script lang="ts">
	import { onMount } from 'svelte';
	import { api, importWorld, ApiError, type WorldImport } from '#lib/api.ts';
	import { bytes, dateTime } from '#lib/format.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';

	const ctx = useServer();

	let history = $state<WorldImport[]>([]);
	let file = $state<File | null>(null);
	let sourceName = $state('');
	let progress = $state<number | null>(null);
	let error = $state('');
	let newerWarning = $state('');
	let done = $state('');
	let exporting = $state(false);

	const stopped = $derived(ctx.status === 'stopped' || ctx.status === 'crashed');

	async function load() {
		history = await api.worldImports(ctx.id).catch(() => []);
	}
	onMount(load);

	function pick(e: Event) {
		file = (e.target as HTMLInputElement).files?.[0] ?? null;
		newerWarning = done = error = '';
		if (file && !sourceName) sourceName = file.name.replace(/\.zip$/i, '');
	}

	async function upload(force = false) {
		if (!file) return;
		if (!force && !confirm(`Replace ${ctx.server?.name}'s world with “${file.name}”? The current world is backed up first.`)) return;
		error = newerWarning = done = '';
		progress = 0;
		try {
			const r = await importWorld(ctx.id, file, { sourceName, force }, (f) => (progress = f));
			done = `Imported “${r.source_name}” (Minecraft ${r.source_mc_version}). Start the server to play it.`;
			file = null;
			sourceName = '';
			await load();
		} catch (e) {
			const err = e as ApiError & { inspection?: { version_name: string } };
			if (err.status === 409 && err.inspection) {
				newerWarning = `This world was saved by a newer Minecraft version (${err.inspection.version_name || 'unknown'}) than the server (${ctx.server?.mc_version}). It may not load or could be damaged.`;
			} else {
				error = err.message;
			}
		} finally {
			progress = null;
		}
	}

	async function exportWorld() {
		exporting = true;
		try {
			await api.exportWorld(ctx.id);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			setTimeout(() => (exporting = false), 1500);
		}
	}
</script>

<svelte:head><title>World · {ctx.server?.name ?? ''} · mcsman</title></svelte:head>

<section class="stack">
	{#if error}<div class="alert error">{error}</div>{/if}
	{#if done}<div class="alert ok">{done}</div>{/if}

	<div class="card stack">
		<h2>Download world</h2>
		<p class="muted small">A zip of the world folders. Works while the server is running (saving is paused briefly).</p>
		<div><button onclick={exportWorld} disabled={exporting}>{exporting ? 'Preparing…' : 'Download world zip'}</button></div>
	</div>

	<div class="card stack">
		<h2>Import a world</h2>
		<p class="muted small">
			Upload a zip of a singleplayer or server world (with <code>level.dat</code> at the top or one folder deep). The current
			world is backed up first.
		</p>
		{#if !stopped}
			<div class="alert warn">Stop the server to import a world.</div>
		{/if}
		<label class="field">
			World zip
			<input type="file" accept=".zip,application/zip" onchange={pick} disabled={!stopped || progress !== null} />
			{#if file}<small>{file.name} · {bytes(file.size)}</small>{/if}
		</label>
		<label class="field">
			Label
			<input bind:value={sourceName} placeholder="e.g. Bob's island" maxlength="100" disabled={progress !== null} />
		</label>
		{#if progress !== null}
			<div class="progress"><div style="width: {Math.round(progress * 100)}%"></div></div>
			<span class="muted small">{progress < 1 ? `Uploading… ${Math.round(progress * 100)}%` : 'Importing…'}</span>
		{/if}
		{#if newerWarning}
			<div class="alert warn stack">
				<span>{newerWarning}</span>
				<div><button class="danger" onclick={() => upload(true)}>Import anyway</button></div>
			</div>
		{/if}
		<div><button class="primary" onclick={() => upload()} disabled={!file || !stopped || progress !== null}>Import world</button></div>
	</div>

	{#if history.length}
		<div class="card stack">
			<h2>Import history</h2>
			<ul class="rows">
				{#each history as h (h.id)}
					<li>
						<strong>{h.source_name}</strong>
						<span class="muted small">Minecraft {h.source_mc_version} · {dateTime(h.imported_at)}</span>
					</li>
				{/each}
			</ul>
		</div>
	{/if}
</section>

<style>
	p {
		margin: 0;
	}
	.small {
		font-size: 0.85rem;
	}
	code {
		font-family: var(--mono);
	}
	.alert.ok {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.progress {
		height: 6px;
		background: var(--surface-2);
		border-radius: 3px;
		overflow: hidden;
	}
	.progress div {
		height: 100%;
		background: var(--accent);
		transition: width 0.2s;
	}
	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.rows li {
		display: flex;
		flex-direction: column;
		padding: 8px 0;
		border-top: 1px solid var(--border);
	}
	input[type='file'] {
		padding: 8px;
	}
</style>
