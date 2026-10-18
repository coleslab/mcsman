<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '#lib/api.ts';

	let versions = $state<string[]>([]);
	let versionsError = $state('');
	let name = $state('');
	let mcVersion = $state('');
	let port = $state(25565);
	let memory = $state(2048);
	let address = $state('');
	let eula = $state(false);
	let error = $state('');
	let busy = $state(false);

	const nameValid = $derived(/^[a-z0-9][a-z0-9_-]{0,31}$/.test(name));

	api
		.paperVersions()
		.then((r) => {
			versions = r.versions;
			mcVersion = r.versions[0] ?? '';
		})
		.catch(() => (versionsError = 'Could not load Paper versions; check the daemon can reach PaperMC.'));

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			const s = await api.createServer({
				name,
				mc_version: mcVersion,
				port,
				max_memory_mb: memory,
				connection_address: address || undefined,
				accept_eula: eula
			});
			goto(`/servers/${s.id}`);
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>New server · mcsman</title></svelte:head>

<main class="container">
	<form class="card stack" onsubmit={submit}>
		<div>
			<a href="/servers" class="muted back">← Servers</a>
			<h1>New Paper server</h1>
		</div>
		{#if error}<div class="alert error" role="alert">{error}</div>{/if}
		{#if versionsError}<div class="alert warn">{versionsError}</div>{/if}

		<label class="field">
			Name
			<input bind:value={name} placeholder="survival" autocapitalize="none" required />
			<small>Lowercase letters, numbers, - and _. Used for the folder name.</small>
		</label>

		<label class="field">
			Minecraft version
			<select bind:value={mcVersion} required disabled={!versions.length}>
				{#each versions as v (v)}<option value={v}>{v}</option>{/each}
			</select>
			<small>The latest Paper build and matching Java runtime are downloaded automatically.</small>
		</label>

		<div class="two">
			<label class="field">
				Port
				<input type="number" bind:value={port} min="1024" max="65535" required />
				<small>RCON uses port + 10.</small>
			</label>
			<label class="field">
				Memory (MB)
				<input type="number" bind:value={memory} min="512" max="65536" step="256" required />
			</label>
		</div>

		<label class="field">
			Connection address <small>(optional)</small>
			<input bind:value={address} placeholder="e.g. survival.playit.gg" autocapitalize="none" />
			<small>What players type into Minecraft. Shown on the server page.</small>
		</label>

		<label class="check">
			<input type="checkbox" bind:checked={eula} required />
			<span>
				I accept the
				<a href="https://aka.ms/MinecraftEULA" target="_blank" rel="noreferrer">Minecraft EULA</a>
			</span>
		</label>

		<button class="primary" type="submit" disabled={busy || !nameValid || !mcVersion || !eula}>
			{busy ? 'Downloading Paper…' : 'Create server'}
		</button>
	</form>
</main>

<style>
	form {
		max-width: 520px;
		margin: 0 auto;
	}
	.back {
		font-size: 0.875rem;
		text-decoration: none;
	}
	.two {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
	}
	.check {
		display: flex;
		gap: 10px;
		align-items: center;
		font-size: 0.9rem;
	}
</style>
