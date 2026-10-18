<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '#lib/api.ts';
	import { until } from '#lib/format.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';

	const ctx = useServer();
	// Initial form values come from a snapshot; display uses the live value.
	const init = ctx.server!;
	const s = $derived(ctx.server!);

	// Preset values for schedule pickers; "custom" reveals a text field.
	const backupPresets = [
		{ value: '', label: `Default (${init.schedule?.default_backup ?? '1h'})` },
		{ value: 'off', label: 'Off' },
		{ value: '1h', label: 'Every hour' },
		{ value: '6h', label: 'Every 6 hours' },
		{ value: '0 4 * * *', label: 'Daily at 04:00' },
		{ value: 'custom', label: 'Custom…' }
	];
	const restartPresets = [
		{ value: '', label: 'Never' },
		{ value: '0 5 * * *', label: 'Daily at 05:00' },
		{ value: '0 5 * * 1', label: 'Weekly (Monday 05:00)' },
		{ value: 'custom', label: 'Custom…' }
	];
	const preset = (list: { value: string }[], v: string) => (list.some((p) => p.value === v) ? v : 'custom');

	let address = $state(init.connection_address ?? '');
	let memory = $state(init.max_memory_mb ?? 2048);
	let port = $state(init.port ?? 25565);
	let rconPort = $state(init.rcon_port ?? 25575);
	let autoRestart = $state(init.auto_restart ?? true);
	let autostart = $state(init.autostart ?? false);
	let backupChoice = $state(preset(backupPresets, init.backup_schedule ?? ''));
	let backupCustom = $state(init.backup_schedule ?? '');
	let keep = $state<number | ''>(init.backup_keep ?? '');
	let restartChoice = $state(preset(restartPresets, init.restart_schedule ?? ''));
	let restartCustom = $state(init.restart_schedule ?? '');

	let error = $state('');
	let saved = $state(false);
	let busy = $state(false);
	let confirmName = $state('');

	const restartNeeded = $derived(
		ctx.status === 'running' && (memory !== s.max_memory_mb || port !== s.port || rconPort !== s.rcon_port)
	);

	async function save(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		saved = false;
		busy = true;
		try {
			const body: Record<string, unknown> = {
				connection_address: address.trim(),
				max_memory_mb: memory,
				port,
				rcon_port: rconPort,
				auto_restart: autoRestart,
				autostart,
				backup_schedule: backupChoice === 'custom' ? backupCustom.trim() : backupChoice,
				backup_keep: keep === '' ? 0 : keep,
				restart_schedule: restartChoice === 'custom' ? restartCustom.trim() : restartChoice
			};
			await api.updateServer(ctx.id, body);
			await ctx.refresh();
			saved = true;
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}

	async function remove() {
		error = '';
		try {
			await api.deleteServer(ctx.id, confirmName);
			goto('/servers');
		} catch (err) {
			error = (err as Error).message;
		}
	}
</script>

<svelte:head><title>Settings · {s.name} · mcsman</title></svelte:head>

<form class="stack" onsubmit={save}>
	{#if error}<div class="alert error">{error}</div>{/if}
	{#if saved}<div class="alert ok">Saved.{restartNeeded ? ' Restart the server to apply port and memory changes.' : ''}</div>{/if}

	<section class="card stack">
		<h2>Connection</h2>
		<label class="field">
			Public address
			<input bind:value={address} placeholder="e.g. survival.playit.gg" autocapitalize="none" maxlength="255" />
			<small>What players type into Minecraft. Shown on the status page and invite confirmations.</small>
		</label>
		<div class="grid3">
			<label class="field">Port <input type="number" bind:value={port} min="1024" max="65535" required /></label>
			<label class="field">RCON port <input type="number" bind:value={rconPort} min="1024" max="65535" required /></label>
			<label class="field">Memory (MB) <input type="number" bind:value={memory} min="512" max="65536" step="256" required /></label>
		</div>
		{#if restartNeeded}<small class="muted">Port and memory changes apply on the next start.</small>{/if}
	</section>

	<section class="card stack">
		<h2>Behaviour</h2>
		<label class="check"><input type="checkbox" bind:checked={autoRestart} /> Restart automatically after a crash</label>
		<label class="check"><input type="checkbox" bind:checked={autostart} /> Start when mcsman starts (e.g. after a reboot)</label>
	</section>

	<section class="card stack">
		<h2>Schedules</h2>
		<div class="grid2">
			<label class="field">
				Automatic backups
				<select bind:value={backupChoice}>
					{#each backupPresets as p (p.value)}<option value={p.value}>{p.label}</option>{/each}
				</select>
				{#if backupChoice === 'custom'}
					<input bind:value={backupCustom} placeholder="e.g. 3h or 0 */2 * * *" autocapitalize="none" />
				{/if}
				<small>
					A stopped server is only backed up if it ran since the last backup.
					{#if s.schedule?.next_backup_at}Next: {until(s.schedule.next_backup_at)}.{/if}
				</small>
			</label>
			<label class="field">
				Keep
				<input type="number" bind:value={keep} min="1" max="1000" placeholder="Default ({s.schedule?.default_keep ?? 24})" />
				<small>Oldest backups beyond this number are deleted.</small>
			</label>
		</div>
		<label class="field">
			Scheduled restart
			<select bind:value={restartChoice}>
				{#each restartPresets as p (p.value)}<option value={p.value}>{p.label}</option>{/each}
			</select>
			{#if restartChoice === 'custom'}
				<input bind:value={restartCustom} placeholder="cron, e.g. 0 5 * * *" autocapitalize="none" />
			{/if}
			<small>
				Players get a 5-minute in-game countdown. Only runs while the server is up.
				{#if s.schedule?.next_restart_at}Next: {until(s.schedule.next_restart_at)}.{/if}
			</small>
		</label>
		<small class="muted">Custom schedules: an interval like <code>6h</code> or a cron expression like <code>0 4 * * *</code> (server's local time).</small>
	</section>

	<div class="actions"><button class="primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save changes'}</button></div>
</form>

<section class="card stack danger-zone">
	<h2>Delete server</h2>
	<p class="muted small">Permanently deletes the server folder, world and all backups. Stop the server first.</p>
	<label class="field">
		<span>Type <strong>{s.name}</strong> to confirm</span>
		<input bind:value={confirmName} autocapitalize="none" autocomplete="off" />
	</label>
	<div>
		<button class="danger solid" type="button" disabled={confirmName !== s.name || !(ctx.status === 'stopped' || ctx.status === 'crashed')} onclick={remove}>
			Delete {s.name}
		</button>
	</div>
</section>

<style>
	.grid3 {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 12px;
	}
	.grid2 {
		display: grid;
		grid-template-columns: 2fr 1fr;
		gap: 12px;
	}
	@media (max-width: 600px) {
		.grid3,
		.grid2 {
			grid-template-columns: 1fr;
		}
	}
	.check {
		display: flex;
		gap: 10px;
		align-items: center;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
	}
	.alert.ok {
		background: var(--accent-soft);
		color: var(--accent);
	}
	code {
		font-family: var(--mono);
	}
	p {
		margin: 0;
	}
	.small {
		font-size: 0.85rem;
	}
	.danger-zone {
		border-color: color-mix(in srgb, var(--danger) 40%, var(--border));
		margin-top: 12px;
	}
</style>
