<script lang="ts">
	import { beforeNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError, type ConfigFile, type ConfigRevision } from '#lib/api.ts';
	import { ago } from '#lib/format.ts';
	import { useServer } from '#lib/serverContext.svelte.ts';

	const ctx = useServer();

	let files = $state<ConfigFile[] | null>(null);
	let path = $state(page.url.searchParams.get('path') ?? '');
	let loaded = $state(''); // content as last read or saved
	let content = $state('');
	let sha = $state('');
	let revisions = $state<ConfigRevision[]>([]);
	let error = $state('');
	let syntaxLine = $state(0);
	let conflict = $state(false);
	let notice = $state('');
	let busy = $state(false);

	const dirty = $derived(content !== loaded);
	const groups = $derived(
		(files ?? []).reduce<Record<string, ConfigFile[]>>((acc, f) => {
			(acc[f.group] ??= []).push(f);
			return acc;
		}, {})
	);

	api.configFiles(ctx.id).then(
		(f) => {
			files = f;
			if (path) open(path);
		},
		(e) => (error = (e as Error).message)
	);

	beforeNavigate((nav) => {
		if (dirty && !confirm('Discard unsaved changes?')) nav.cancel();
	});

	function reset() {
		error = notice = '';
		syntaxLine = 0;
		conflict = false;
	}

	async function open(p: string) {
		reset();
		busy = true;
		try {
			const [f, revs] = await Promise.all([api.readConfig(ctx.id, p), api.configRevisions(ctx.id, p)]);
			path = p;
			loaded = content = f.content;
			sha = f.sha256;
			revisions = revs;
			goto(`?path=${encodeURIComponent(p)}`, { replace: true, shallow: true });
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	function choose(e: Event) {
		const next = (e.target as HTMLSelectElement).value;
		if (dirty && !confirm('Discard unsaved changes?')) {
			(e.target as HTMLSelectElement).value = path;
			return;
		}
		if (next) open(next);
	}

	async function save(force = false) {
		reset();
		busy = true;
		try {
			const r = await api.writeConfig(ctx.id, path, content, sha, force);
			sha = r.sha256;
			loaded = content;
			revisions = await api.configRevisions(ctx.id, path);
			notice = r.restart_required
				? 'Saved. Restart the server (or reload the plugin from the Console) for it to take effect.'
				: 'Saved.';
		} catch (e) {
			error = (e as Error).message;
			if (e instanceof ApiError && e.status === 409) conflict = true;
			if (e instanceof ApiError && e.status === 422) syntaxLine = -1;
		} finally {
			busy = false;
		}
	}

	/** Keeps the editor's text but takes the file's current version as the base. */
	async function overwrite() {
		const mine = content;
		const f = await api.readConfig(ctx.id, path);
		sha = f.sha256;
		content = mine;
		await save();
	}

	async function loadRevision(e: Event) {
		const sel = e.target as HTMLSelectElement;
		const id = sel.value;
		sel.value = '';
		if (!id || (dirty && !confirm('Replace your unsaved changes with this version?'))) return;
		try {
			content = (await api.configRevision(ctx.id, id)).content;
			notice = 'Loaded an earlier version into the editor. Save to restore it.';
		} catch (err) {
			error = (err as Error).message;
		}
	}
</script>

<section class="card stack">
	<div class="bar">
		<label class="field grow">
			File
			<select value={path} onchange={choose} disabled={!files}>
				<option value="">{files ? 'Choose a file…' : 'Loading…'}</option>
				{#each Object.entries(groups) as [group, list] (group)}
					<optgroup label={group}>
						{#each list as f (f.path)}<option value={f.path}>{f.path}</option>{/each}
					</optgroup>
				{/each}
			</select>
		</label>
		{#if path && revisions.length}
			<label class="field">
				History
				<select onchange={loadRevision}>
					<option value="">Earlier versions…</option>
					{#each revisions as r (r.id)}
						<option value={r.id}>{ago(r.created_at)} ago{r.username ? ` · before ${r.username}'s edit` : ''}</option>
					{/each}
				</select>
			</label>
		{/if}
	</div>

	{#if files && files.length === 0}
		<p class="muted">No editable config files yet. Start the server once so Paper and plugins create them.</p>
	{/if}
	{#if error}
		<div class="alert error" role="alert">
			{error}
			{#if conflict}
				<div class="btns">
					<button class="small" onclick={() => open(path)}>Reload (discard mine)</button>
					<button class="small danger" onclick={overwrite}>Overwrite with mine</button>
				</div>
			{:else if syntaxLine}
				<div class="btns"><button class="small danger" onclick={() => save(true)}>Save anyway</button></div>
			{/if}
		</div>
	{/if}
	{#if notice}<div class="alert ok">{notice}</div>{/if}

	{#if path && sha}
		{#if path === 'server.properties'}
			<p class="muted small">
				The port and RCON settings are managed by mcsman (Settings tab) and stay as they are; the RCON password is
				hidden.
			</p>
		{/if}
		<textarea
			class="editor"
			bind:value={content}
			spellcheck="false"
			autocapitalize="none"
			autocomplete="off"
			wrap="off"
			aria-label="File contents"
		></textarea>
		<div class="btns">
			<button class="primary" disabled={busy || !dirty} onclick={() => save()}>{busy ? 'Saving…' : 'Save'}</button>
			<button disabled={busy || !dirty} onclick={() => (content = loaded)}>Revert</button>
			{#if dirty}<span class="muted small">Unsaved changes</span>{/if}
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
	.bar {
		display: flex;
		flex-wrap: wrap;
		gap: 12px;
		align-items: end;
	}
	.grow {
		flex: 1;
		min-width: 220px;
	}
	.editor {
		width: 100%;
		min-height: 420px;
		font-family: var(--mono);
		font-size: 0.85rem;
		line-height: 1.45;
		tab-size: 2;
		resize: vertical;
		white-space: pre;
		overflow: auto;
	}
	.btns {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
		margin-top: 8px;
	}
	.alert .btns {
		margin-top: 8px;
	}
	.alert.ok {
		background: var(--accent-soft);
		color: var(--accent);
	}
</style>
