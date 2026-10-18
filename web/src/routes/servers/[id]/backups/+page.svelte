<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Backup } from '#lib/api.ts';
	import { bytes, ago, dateTime } from '#lib/format.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';

	const ctx = useServer();

	let backups = $state<Backup[] | null>(null);
	let error = $state('');
	let busy = $state('');

	const progress = $derived(live.backup[ctx.id] ?? null);
	const stopped = $derived(ctx.status === 'stopped' || ctx.status === 'crashed');

	async function load() {
		try {
			backups = await api.backups(ctx.id);
		} catch (e) {
			error = (e as Error).message;
		}
	}

	onMount(() => {
		load();
		return live.on((e) => {
			if (e.server_id !== ctx.id) return;
			if (e.type === 'backup_complete') load();
			if (e.type === 'backup_failed') error = `Backup failed: ${(e.data as { error: string }).error}`;
		});
	});

	async function run(name: string, fn: () => Promise<unknown>) {
		busy = name;
		error = '';
		try {
			await fn();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	function create() {
		run('create', () => api.createBackup(ctx.id, 'manual'));
	}

	function restore(b: Backup) {
		if (!confirm(`Restore ${dateTime(b.created_at)}? The current world is backed up first, then replaced.`)) return;
		run(b.id, async () => {
			await api.restoreBackup(ctx.id, b.id);
			await load();
		});
	}

	function remove(b: Backup) {
		if (!confirm(`Delete the backup from ${dateTime(b.created_at)}? This cannot be undone.`)) return;
		run(b.id, async () => {
			await api.deleteBackup(ctx.id, b.id);
			await load();
		});
	}

	const phaseLabel: Record<string, string> = {
		flush: 'Saving world…',
		archive: 'Compressing…',
		verify: 'Verifying…'
	};
</script>

<svelte:head><title>Backups · {ctx.server?.name ?? ''} · mcsman</title></svelte:head>

<section class="stack">
	<div class="row">
		<p class="muted intro">Backups include the world folders. Restoring requires the server to be stopped.</p>
		<span class="spacer"></span>
		<button class="primary" onclick={create} disabled={!!progress || busy === 'create'}>Back up now</button>
	</div>

	{#if progress}
		<div class="card progress">
			<span>{phaseLabel[progress.phase] ?? progress.phase}</span>
			<span class="muted">{bytes(progress.bytes_written)}</span>
		</div>
	{/if}
	{#if error}<div class="alert error">{error}</div>{/if}

	{#if backups === null}
		<p class="muted">Loading…</p>
	{:else if backups.length === 0}
		<div class="card empty">No backups yet.</div>
	{:else}
		<ul class="list card">
			{#each backups as b (b.id)}
				<li>
					<div class="info">
						<div class="row">
							<strong>{dateTime(b.created_at)}</strong>
							{#if b.tag}<span class="tag">{b.tag}</span>{/if}
						</div>
						<span class="muted small">{bytes(b.size_bytes)} · {ago(b.created_at)}</span>
					</div>
					<div class="btns">
						<button class="small" onclick={() => api.downloadBackup(ctx.id, b, ctx.server?.name ?? 'server')}>
							Download
						</button>
						<button
							class="small"
							disabled={!stopped || !!busy}
							title={stopped ? '' : 'Stop the server to restore'}
							onclick={() => restore(b)}
						>
							{busy === b.id ? 'Working…' : 'Restore'}
						</button>
						<button class="small danger" disabled={!!busy} onclick={() => remove(b)}>Delete</button>
					</div>
				</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	.intro {
		margin: 0;
		font-size: 0.9rem;
	}
	.progress {
		display: flex;
		justify-content: space-between;
	}
	.list {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.list li {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border);
	}
	.list li:last-child {
		border-bottom: 0;
	}
	.info {
		flex: 1;
		min-width: 180px;
		display: flex;
		flex-direction: column;
	}
	.small {
		font-size: 0.8rem;
	}
	.tag {
		font-size: 0.75rem;
		padding: 1px 8px;
		border-radius: 999px;
		background: var(--surface-2);
		color: var(--text-2);
	}
	.btns {
		display: flex;
		gap: 6px;
	}
</style>
