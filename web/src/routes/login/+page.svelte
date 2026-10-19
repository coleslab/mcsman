<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { ApiError } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';

	let username = $state('');
	let password = $state('');
	let error = $state('');
	let busy = $state(false);

	function nextPath() {
		const next = page.url.searchParams.get('next') ?? '/servers';
		// Only allow same-site relative paths.
		// Browsers treat "/\" like "//", so reject both.
		return next.startsWith('/') && !/^\/[/\\]/.test(next) && !next.startsWith('/login') ? next : '/servers';
	}

	$effect(() => {
		if (session.loaded && session.user) goto(nextPath(), { replaceState: true });
	});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			await session.login(username.trim(), password);
			goto(nextPath(), { replaceState: true });
		} catch (err) {
			error =
				err instanceof ApiError && err.status === 429
					? 'Too many attempts. Wait a minute and try again.'
					: err instanceof ApiError && err.status === 401
						? 'Wrong username or password.'
						: 'Could not reach the server.';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Log in · mcsman</title></svelte:head>

<main>
	<form class="card stack" onsubmit={submit}>
		<div class="head">
			<img src="/favicon.svg" alt="" width="40" height="40" />
			<h1>mcsman</h1>
			<p class="muted">Sign in to manage your servers</p>
		</div>
		{#if error}<div class="alert error" role="alert">{error}</div>{/if}
		<label class="field">
			Username
			<input bind:value={username} autocomplete="username" autocapitalize="none" required />
		</label>
		<label class="field">
			Password
			<input type="password" bind:value={password} autocomplete="current-password" required />
		</label>
		<button class="primary" type="submit" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
	</form>
</main>

<style>
	main {
		min-height: 100dvh;
		display: grid;
		place-items: center;
		padding: 16px;
	}
	form {
		width: 100%;
		max-width: 360px;
		padding: 24px;
	}
	.head {
		text-align: center;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 6px;
		margin-bottom: 4px;
	}
	.head p {
		margin: 0;
		font-size: 0.9rem;
	}
</style>
