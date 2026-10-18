<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type Preflight, type UpgradeJob } from '#lib/api.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';

	const ctx = useServer();

	let versions = $state<string[]>([]);
	let target = $state('');
	let report = $state<Preflight | null>(null);
	let job = $state<UpgradeJob | null>(null);
	let error = $state('');
	let busy = $state('');

	const newer = $derived(
		versions.filter((v) => ctx.server && compare(v, ctx.server.mc_version) >= 0)
	);

	function compare(a: string, b: string) {
		const as = a.split('.').map(Number), bs = b.split('.').map(Number);
		for (let i = 0; i < Math.max(as.length, bs.length); i++) {
			const d = (as[i] ?? 0) - (bs[i] ?? 0);
			if (d) return d;
		}
		return 0;
	}

	async function refreshJob() {
		job = (await api.upgradeStatus(ctx.id).catch(() => ({ job: null }))).job;
	}

	onMount(() => {
		api.paperVersions().then((r) => {
			versions = r.versions;
			target = newer.find((v) => v !== ctx.server?.mc_version) ?? newer[0] ?? '';
		});
		refreshJob();
		return live.on((e) => {
			if (e.server_id !== ctx.id) return;
			if (e.type === 'upgrade_progress' || e.type === 'upgrade_complete') {
				refreshJob();
				if (e.type === 'upgrade_complete') ctx.refresh();
			}
		});
	});

	async function check() {
		busy = 'check';
		error = '';
		report = null;
		try {
			report = await api.preflight(ctx.id, target);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	async function upgrade() {
		if (!report) return;
		const players = ctx.metrics?.player_count ?? 0;
		const msg =
			`Upgrade ${ctx.server?.name} to ${report.target_version}?\n\n` +
			(players ? `${players} player(s) will get a 60-second warning. ` : '') +
			'The server will be stopped and backed up first.';
		if (!confirm(msg)) return;
		busy = 'upgrade';
		error = '';
		try {
			job = await api.confirmUpgrade(ctx.id, report.target_version);
			report = null;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	const label: Record<string, string> = {
		green: 'Compatible',
		amber: 'No release for this version',
		gray: 'Manual — not checked'
	};
</script>

<svelte:head><title>Version · {ctx.server?.name ?? ''} · mcsman</title></svelte:head>

<section class="stack">
	{#if error}<div class="alert error">{error}</div>{/if}

	{#if job && (job.state === 'running' || Date.now() / 1000 - (job.finished_at ?? 0) < 3600)}
		<div class="card stack job {job.state}">
			<div class="row">
				<h2>
					{job.state === 'running' ? 'Upgrading' : job.state === 'succeeded' ? 'Upgraded' : 'Upgrade failed'}
					{job.from} → {job.to}
				</h2>
			</div>
			<ol class="steps">
				{#each job.steps as s, i (i)}
					<li class:current={job.state === 'running' && i === job.steps.length - 1}>
						<span class="phase">{s.phase}</span>
						<span>{s.detail}</span>
					</li>
				{/each}
			</ol>
			{#if job.plugins_updated.length}
				<p class="small">Updated: {job.plugins_updated.join(', ')}</p>
			{/if}
			{#if job.plugin_errors.length}
				<div class="alert warn stack">
					<strong>Plugin errors after start</strong>
					{#each job.plugin_errors as e, i (i)}<code class="err">{e}</code>{/each}
				</div>
			{/if}
			{#if job.state === 'failed' && job.backup_id}
				<p class="small muted">The world was backed up before the upgrade. You can restore it from the Backups tab.</p>
			{/if}
		</div>
	{/if}

	<div class="card stack">
		<h2>Paper version</h2>
		<p class="muted">Currently running <strong>Paper {ctx.server?.mc_version}</strong> on Java {ctx.server?.java_version}.</p>
		<div class="pick">
			<select bind:value={target} disabled={!newer.length}>
				{#each newer as v (v)}
					<option value={v}>{v}{v === ctx.server?.mc_version ? ' (current — latest build)' : ''}</option>
				{/each}
			</select>
			<button onclick={check} disabled={!target || busy === 'check' || job?.state === 'running'}>
				{busy === 'check' ? 'Checking…' : 'Check compatibility'}
			</button>
		</div>
	</div>

	{#if report}
		<div class="card stack">
			<h2>Pre-flight: {report.current_version} → {report.target_version} <span class="muted">(build {report.target_build})</span></h2>
			{#if report.java_change}
				<p>Java {report.current_java} → <strong>{report.required_java}</strong> (installed automatically)</p>
			{/if}
			{#if report.plugins.length}
				<ul class="compat">
					{#each report.plugins as c (c.plugin.id)}
						<li>
							<span class="dot {c.status}"></span>
							<strong>{c.plugin.name}</strong>
							<span class="muted small">{c.plugin.version}</span>
							<span class="spacer"></span>
							<span class="small {c.status}-text">
								{#if c.status === 'green' && c.release && c.release.version !== c.plugin.version}
									Will update to {c.release.version}
								{:else}
									{label[c.status]}
								{/if}
							</span>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="muted small">No plugins installed.</p>
			{/if}
			{#each report.warnings as w, i (i)}<div class="alert warn">{w}</div>{/each}
			{#each report.blockers as b, i (i)}<div class="alert error">{b}</div>{/each}
			<button class="primary" onclick={upgrade} disabled={!report.can_upgrade || busy === 'upgrade'}>
				{busy === 'upgrade' ? 'Starting…' : `Upgrade to ${report.target_version}`}
			</button>
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
	.pick {
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}
	.pick select {
		flex: 1;
		min-width: 180px;
	}
	.compat {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.compat li {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 0;
		border-top: 1px solid var(--border);
		flex-wrap: wrap;
	}
	.dot {
		width: 10px;
		height: 10px;
		border-radius: 50%;
	}
	.dot.green {
		background: var(--accent);
	}
	.dot.amber {
		background: var(--warn);
	}
	.dot.gray {
		background: var(--text-2);
	}
	.green-text {
		color: var(--accent);
	}
	.amber-text {
		color: var(--warn);
	}
	.gray-text {
		color: var(--text-2);
	}
	.job.running {
		border-color: var(--warn);
	}
	.job.succeeded {
		border-color: var(--accent);
	}
	.job.failed {
		border-color: var(--danger);
	}
	.steps {
		margin: 0;
		padding-left: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-size: 0.9rem;
	}
	.steps li {
		display: flex;
		gap: 10px;
	}
	.steps .phase {
		width: 72px;
		flex-shrink: 0;
		color: var(--text-2);
		font-family: var(--mono);
		font-size: 0.8rem;
		padding-top: 2px;
	}
	.steps li.current span:last-child::after {
		content: ' …';
	}
	.err {
		font-family: var(--mono);
		font-size: 0.78rem;
		overflow-wrap: anywhere;
	}
</style>
