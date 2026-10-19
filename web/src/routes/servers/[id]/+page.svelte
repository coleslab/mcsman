<script lang="ts">
	import { api, type PluginSummary, type ServerEvent } from '#lib/api.ts';
	import { ago, until } from '#lib/format.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';
	import { session } from '#lib/session.svelte.ts';

	const ctx = useServer();
	let copied = $state(false);
	let plugins = $state<PluginSummary[] | null>(null);
	let events = $state<ServerEvent[] | null>(null);

	// Admins have the Plugins tab; members see the list here.
	if (!session.isAdmin) api.pluginSummaries(ctx.id).then((p) => (plugins = p), () => (plugins = []));
	api.serverEvents(ctx.id).then((e) => (events = e.slice(0, 10)), () => (events = []));

	const eventLabels: Record<string, string> = {
		start: 'Started',
		stop: 'Stopped',
		crash: 'Crashed',
		restart: 'Restarted after a crash',
		backup: 'Backup',
		upgrade: 'Upgraded'
	};

	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			/* clipboard unavailable (insecure context) */
		}
	}

	function tpsClass(tps: number) {
		return tps >= 19 ? 'good' : tps >= 15 ? 'warn' : 'bad';
	}
</script>

{#if ctx.server}
	{@const s = ctx.server}
	{@const m = ctx.metrics}
	<section class="tiles">
		<div class="card tile">
			<span class="label">TPS</span>
			<span class="value {m ? tpsClass(m.tps) : ''}">{m ? m.tps.toFixed(1) : '—'}</span>
		</div>
		<div class="card tile">
			<span class="label">MSPT</span>
			<span class="value">{m ? m.mspt.toFixed(1) : '—'}</span>
		</div>
		<div class="card tile">
			<span class="label">Memory</span>
			<span class="value">{m ? `${(m.ram_used_mb / 1024).toFixed(1)} GB` : '—'}</span>
			{#if m}
				<!-- RSS includes JVM overhead beyond the heap, so it can exceed the limit. -->
				<span class="sub muted">process · heap limit {(m.ram_max_mb / 1024).toFixed(1)} GB</span>
			{/if}
		</div>
		<div class="card tile">
			<span class="label">Players</span>
			<span class="value">{m ? m.player_count : '—'}</span>
		</div>
	</section>

	{#if ctx.status !== 'running'}
		<p class="muted note">Live metrics appear while the server is running (updated every 10 seconds).</p>
	{/if}

	{#if s.last_backup && !s.last_backup.ok}
		<div class="alert error">
			<strong>Last backup failed</strong> ({ago(s.last_backup.at)}): {s.last_backup.error}
		</div>
	{/if}

	<section class="card stack">
		<h2>Connect</h2>
		{#if s.connection_address}
			<div class="row">
				<code class="addr">{s.connection_address}</code>
				<button class="small" onclick={() => copy(s.connection_address!)}>{copied ? 'Copied' : 'Copy'}</button>
			</div>
		{:else if s.port}
			<p class="muted">No public address set. Players on your network can use this machine's IP and port {s.port}.</p>
		{:else}
			<p class="muted">No public address set. Ask an admin how to connect.</p>
		{/if}
		{#if s.players.length}
			<div class="players">
				{#each s.players as p (p)}<span class="player">{p}</span>{/each}
			</div>
		{/if}
	</section>

	{#if !session.isAdmin}
		<section class="card">
			<h2>Details</h2>
			<dl>
				<dt>Version</dt><dd>Paper {s.mc_version}</dd>
				<dt>Online</dt><dd>{m ? `${m.player_count} player${m.player_count === 1 ? '' : 's'}` : '—'}</dd>
			</dl>
		</section>

		<section class="card">
			<h2>Plugins</h2>
			{#if plugins === null}
				<p class="muted">Loading…</p>
			{:else if plugins.length === 0}
				<p class="muted">No plugins installed.</p>
			{:else}
				<ul class="plain">
					{#each plugins as p (p.name)}
						<li><span>{p.name}</span><span class="muted">{p.version}</span></li>
					{/each}
				</ul>
			{/if}
		</section>
	{/if}

	<section class="card">
		<h2>Recent activity</h2>
		{#if events === null}
			<p class="muted">Loading…</p>
		{:else if events.length === 0}
			<p class="muted">Nothing yet.</p>
		{:else}
			<ul class="plain">
				{#each events as e (e.id)}
					<li><span>{eventLabels[e.type] ?? e.type}</span><span class="muted">{ago(e.created_at)}</span></li>
				{/each}
			</ul>
		{/if}
	</section>

	{#if session.isAdmin}
	<section class="card">
		<h2>Details</h2>
		<dl>
			<dt>Paper</dt><dd>{s.mc_version}</dd>
			<dt>Java</dt><dd>{s.java_version}</dd>
			<dt>Port</dt><dd>{s.port} <span class="muted">(RCON {s.rcon_port})</span></dd>
			<dt>Memory</dt><dd>{s.max_memory_mb} MB</dd>
			<dt>Auto-restart</dt><dd>{s.auto_restart ? 'On' : 'Off'}</dd>
			<dt>Backups</dt>
			<dd>
				{#if s.schedule?.next_backup_at}
					next {until(s.schedule.next_backup_at)}
				{:else}
					not scheduled
				{/if}
				{#if s.last_backup?.ok}<span class="muted"> · last {ago(s.last_backup.at)}</span>{/if}
			</dd>
			{#if s.schedule?.next_restart_at}
				<dt>Restart</dt><dd>next {until(s.schedule.next_restart_at)}</dd>
			{/if}
			<dt>Folder</dt><dd class="mono path">{s.directory}</dd>
		</dl>
	</section>
	{/if}
{/if}

<style>
	.tiles {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 12px;
	}
	@media (min-width: 720px) {
		.tiles {
			grid-template-columns: repeat(4, 1fr);
		}
	}
	.tile {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.label {
		font-size: 0.8rem;
		color: var(--text-2);
		font-weight: 550;
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.value {
		font-size: 1.6rem;
		font-weight: 650;
		font-variant-numeric: tabular-nums;
	}
	.value.good {
		color: var(--accent);
	}
	.value.warn {
		color: var(--warn);
	}
	.value.bad {
		color: var(--danger);
	}
	.sub {
		font-size: 0.75rem;
	}
	.note {
		margin: 0;
		font-size: 0.875rem;
	}
	.addr {
		font-family: var(--mono);
		font-size: 1rem;
		padding: 8px 12px;
		background: var(--surface-2);
		border-radius: var(--radius-sm);
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.players {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}
	.player {
		padding: 2px 10px;
		background: var(--accent-soft);
		color: var(--accent);
		border-radius: 999px;
		font-size: 0.85rem;
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 6px 16px;
		margin: 12px 0 0;
		font-size: 0.9rem;
	}
	dt {
		color: var(--text-2);
	}
	dd {
		margin: 0;
		min-width: 0;
	}
	.path {
		overflow-wrap: anywhere;
		font-size: 0.8rem;
	}
	.plain {
		list-style: none;
		margin: 12px 0 0;
		padding: 0;
		font-size: 0.9rem;
	}
	.plain li {
		display: flex;
		justify-content: space-between;
		gap: 12px;
		padding: 6px 0;
		border-top: 1px solid var(--border);
	}
	.plain li:first-child {
		border-top: none;
	}
</style>
