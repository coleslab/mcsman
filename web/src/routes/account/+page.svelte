<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, ApiError } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';

	async function logout() {
		live.stop();
		await session.logout();
		goto('/status');
	}

	let current = $state('');
	let next = $state('');
	let confirmPw = $state('');
	let error = $state('');
	let ok = $state(false);
	let busy = $state(false);

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		ok = false;
		if (next !== confirmPw) {
			error = 'New passwords do not match.';
			return;
		}
		busy = true;
		try {
			await api.changePassword(current, next);
			ok = true;
			current = next = confirmPw = '';
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Could not reach the server.';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Account · mcsman</title></svelte:head>

<main class="container stack narrow">
	<h1>Account</h1>
	{#if session.user}
		<section class="card">
			<dl>
				<dt>Username</dt><dd>{session.user.username}</dd>
				<dt>Minecraft</dt><dd>{session.user.mc_username ?? '—'}</dd>
				<dt>Role</dt><dd class="cap">{session.user.role}</dd>
			</dl>
			<div class="out"><button onclick={logout}>Log out</button></div>
		</section>
	{/if}

	<form class="card stack" onsubmit={submit}>
		<h2>Change password</h2>
		{#if error}<div class="alert error" role="alert">{error}</div>{/if}
		{#if ok}<div class="alert ok">Password changed. Other devices have been signed out.</div>{/if}
		<label class="field">
			Current password
			<input type="password" bind:value={current} autocomplete="current-password" required />
		</label>
		<label class="field">
			New password
			<input type="password" bind:value={next} autocomplete="new-password" minlength="8" maxlength="72" required />
			<small>At least 8 characters.</small>
		</label>
		<label class="field">
			Confirm new password
			<input type="password" bind:value={confirmPw} autocomplete="new-password" required />
		</label>
		<button class="primary" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Change password'}</button>
	</form>
</main>

<style>
	.narrow {
		max-width: 520px;
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 6px 16px;
		margin: 0;
	}
	dt {
		color: var(--text-2);
	}
	dd {
		margin: 0;
	}
	.out {
		margin-top: 12px;
	}
	.cap {
		text-transform: capitalize;
	}
	.alert.ok {
		background: var(--accent-soft);
		color: var(--accent);
	}
</style>
