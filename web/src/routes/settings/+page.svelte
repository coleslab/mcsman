<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, type Server, type Webhook } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';

	let hooks = $state<Webhook[] | null>(null);
	let servers = $state<Server[]>([]);
	let newScope = $state<string[]>([]); // empty = all servers
	let keys = $state<string[]>([]);
	let error = $state('');
	let notice = $state('');
	let busy = $state('');
	let name = $state('');
	let url = $state('');

	const labels: Record<string, string> = {
		server_online: 'Server online',
		server_offline: 'Server offline',
		server_crash: 'Crash',
		restart_warning: 'Restart warning',
		backup_failed: 'Backup failed',
		backup_complete: 'Backup complete',
		upgrade_complete: 'Upgrade finished',
		player_join: 'Player joined',
		player_leave: 'Player left'
	};

	async function load() {
		try {
			const [r, s] = await Promise.all([api.webhooks(), api.servers()]);
			hooks = r.webhooks;
			keys = r.event_keys;
			servers = s;
		} catch (e) {
			error = (e as Error).message;
		}
	}

	$effect(() => {
		if (session.isAdmin) load();
		else goto('/status', { replaceState: true });
	});

	async function run(key: string, fn: () => Promise<unknown>, okMsg = '') {
		busy = key;
		error = notice = '';
		try {
			await fn();
			notice = okMsg;
			await load();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	function add(e: SubmitEvent) {
		e.preventDefault();
		run('add', async () => {
			await api.createWebhook(name.trim(), url.trim(), newScope);
			name = url = '';
			newScope = [];
		}, 'Webhook added.');
	}

	function setAll(h: Webhook) {
		run(h.id, () => api.updateWebhook(h.id, { server_ids: [] }));
	}

	function setOnly(h: Webhook) {
		// Start a scoped webhook with the first server selected.
		if (h.all_servers && servers.length) run(h.id, () => api.updateWebhook(h.id, { server_ids: [servers[0].id] }));
	}

	function toggleServer(h: Webhook, id: string) {
		const next = h.server_ids.includes(id) ? h.server_ids.filter((x) => x !== id) : [...h.server_ids, id];
		if (next.length === 0) {
			error = 'Pick at least one server, or choose “All servers”.';
			return;
		}
		run(h.id, () => api.updateWebhook(h.id, { server_ids: next }));
	}

	function toggle(h: Webhook, key: string) {
		const events = { ...h.events, [key]: !h.events[key] };
		h.events = events; // optimistic
		run(h.id, () => api.updateWebhook(h.id, { events }));
	}
</script>

<svelte:head><title>Settings · mcsman</title></svelte:head>

<main class="container stack">
	<h1>Settings</h1>
	{#if error}<div class="alert error">{error}</div>{/if}
	{#if notice}<div class="alert ok">{notice}</div>{/if}

	<section class="card stack">
		<div>
			<h2>Discord notifications</h2>
			<p class="muted small">
				In Discord: Server Settings → Integrations → Webhooks → New Webhook → Copy URL. Messages are sent with the server's
				name as the sender. Webhook URLs are stored encrypted and never shown in full again.
			</p>
		</div>

		{#if hooks === null}
			<p class="muted">Loading…</p>
		{:else}
			{#each hooks as h (h.id)}
				<div class="hook stack">
					<div class="row">
						<strong>{h.name}</strong>
						<code class="muted small">{h.url_masked}</code>
						<span class="spacer"></span>
						<button class="small" disabled={!!busy} onclick={() => run(h.id, () => api.testWebhook(h.id), `Test message sent to ${h.name}.`)}>
							Send test
						</button>
						<button
							class="small danger"
							disabled={!!busy}
							onclick={() => confirm(`Delete webhook ${h.name}?`) && run(h.id, () => api.deleteWebhook(h.id))}
						>
							Delete
						</button>
					</div>
					{#if servers.length > 0 || !h.all_servers}
						<div class="scope">
							<span class="muted small">Servers</span>
							<label class="ev">
								<input type="radio" name="scope-{h.id}" checked={h.all_servers} disabled={!!busy} onchange={() => setAll(h)} />
								<span>All servers</span>
							</label>
							<label class="ev">
								<input type="radio" name="scope-{h.id}" checked={!h.all_servers} disabled={!!busy} onchange={() => setOnly(h)} />
								<span>Only:</span>
							</label>
							{#if !h.all_servers}
								{#each servers as s (s.id)}
									<label class="ev chip">
										<input type="checkbox" checked={h.server_ids.includes(s.id)} disabled={!!busy} onchange={() => toggleServer(h, s.id)} />
										<span>{s.name}</span>
									</label>
								{/each}
								{#if h.server_ids.length === 0}
									<span class="alert warn small">Its servers were deleted — this webhook is inactive.</span>
								{/if}
							{/if}
						</div>
					{/if}
					<div class="events">
						{#each keys as k (k)}
							<label class="ev">
								<input type="checkbox" checked={h.events[k]} disabled={busy === h.id} onchange={() => toggle(h, k)} />
								<span>{labels[k] ?? k}</span>
							</label>
						{/each}
					</div>
				</div>
			{/each}
			{#if hooks.length === 0}<p class="muted small">No webhooks yet.</p>{/if}
		{/if}

		<form class="add" onsubmit={add}>
			<label class="field">
				Name
				<input bind:value={name} placeholder="server-status" required maxlength="50" />
			</label>
			<label class="field url">
				Webhook URL
				<input bind:value={url} type="url" placeholder="https://discord.com/api/webhooks/…" required autocomplete="off" />
			</label>
			{#if servers.length > 0}
				<div class="scope new">
					<span class="muted small">Servers</span>
					<label class="ev">
						<input type="radio" name="scope-new" checked={newScope.length === 0} onchange={() => (newScope = [])} />
						<span>All</span>
					</label>
					{#each servers as s (s.id)}
						<label class="ev chip">
							<input
								type="checkbox"
								checked={newScope.includes(s.id)}
								onchange={() => (newScope = newScope.includes(s.id) ? newScope.filter((x) => x !== s.id) : [...newScope, s.id])}
							/>
							<span>{s.name}</span>
						</label>
					{/each}
				</div>
			{/if}
			<button class="primary add-btn" type="submit" disabled={busy === 'add'}>Add webhook</button>
		</form>
	</section>
</main>

<style>
	p {
		margin: 0;
	}
	.small {
		font-size: 0.85rem;
	}
	.alert.ok {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.hook {
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		gap: 10px;
	}
	code {
		font-family: var(--mono);
		overflow-wrap: anywhere;
	}
	.events {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
		gap: 6px 12px;
	}
	.ev {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 0.9rem;
	}
	.scope {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 6px 14px;
		padding-bottom: 8px;
		border-bottom: 1px solid var(--border);
	}
	.add-btn {
		grid-column: 1 / -1;
		justify-self: end;
	}
	.scope.new {
		grid-column: 1 / -1;
		border-bottom: 0;
		padding-bottom: 0;
	}
	.chip {
		padding: 2px 10px 2px 6px;
		border-radius: 999px;
		background: var(--surface-2);
	}
	input[type='radio'] {
		width: 18px;
		min-height: 18px;
		accent-color: var(--accent);
	}
	.add {
		display: grid;
		grid-template-columns: 1fr 2fr;
		gap: 12px;
		align-items: end;
	}
	@media (max-width: 640px) {
		.add {
			grid-template-columns: 1fr;
		}
		.add-btn {
			justify-self: stretch;
		}
	}
</style>
