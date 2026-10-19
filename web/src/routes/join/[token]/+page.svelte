<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError, type InviteInfo, type WhitelistedServer } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import CopyField from '#lib/CopyField.svelte';

	const token = page.params.token ?? '';

	let info = $state<InviteInfo | null>(null);
	let invalid = $state('');
	let username = $state('');
	let password = $state('');
	let confirmPw = $state('');
	let mcUsername = $state('');
	let error = $state('');
	let busy = $state(false);
	let done = $state<{ server: WhitelistedServer | null; account: string } | null>(null);

	const mcValid = $derived(mcUsername === '' || /^[A-Za-z0-9_]{3,16}$/.test(mcUsername));
	// Signed in already: a server invite is accepted into this account.
	const me = $derived(session.loaded ? session.user : null);
	const needsMC = $derived(!me?.mc_username);

	async function accept(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			const server = await api.acceptInvite(token, needsMC ? mcUsername.trim() : undefined);
			await session.load();
			done = { server, account: session.user?.mc_username ?? me?.username ?? '' };
		} catch (err) {
			if (err instanceof ApiError && err.status === 410) invalid = err.message;
			else error = err instanceof ApiError ? err.message : 'Could not reach the server.';
		} finally {
			busy = false;
		}
	}

	api
		.inviteInfo(token)
		.then((i) => (info = i))
		.catch((e) => {
			invalid =
				e instanceof ApiError && e.status === 429
					? 'Too many attempts. Wait a minute and reload.'
					: 'This invite link is invalid, already used, or has expired. Ask an admin for a new one.';
		});

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		if (password !== confirmPw) {
			error = 'Passwords do not match.';
			return;
		}
		busy = true;
		try {
			const server = await api.redeem(token, {
				username: username.trim(),
				password,
				mc_username: mcUsername.trim() || undefined
			});
			await session.load();
			done = { server, account: mcUsername.trim() || username.trim() };
		} catch (err) {
			if (err instanceof ApiError && err.status === 410) invalid = err.message;
			else error = err instanceof ApiError ? err.message : 'Could not reach the server.';
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head><title>Join · mcsman</title></svelte:head>

<main>
	<div class="card stack box">
		<div class="head">
			<img src="/favicon.svg" alt="" width="40" height="40" />
		</div>

		{#if done}
			{#if done.server}
				<h1>Welcome to {done.server.name}!</h1>
				{#if done.server.connection_address}
					<p>You're all set. Connect in Minecraft using:</p>
					<CopyField value={done.server.connection_address} large />
				{:else}
					<p>You're all set — ask the admin for the server address.</p>
				{/if}
				{#if done.server.queued}
					<p class="muted small">The server is offline right now; you'll be whitelisted as soon as it starts.</p>
				{/if}
				<p class="muted">Your account: <strong>{done.account}</strong></p>
			{:else}
				<h1>Welcome! Your account is ready.</h1>
				<p>Check the status page to see available servers.</p>
			{/if}
			{#if done.server}
				<a class="btn primary" href="/servers">Go to my servers</a>
			{:else}
				<a class="btn primary" href="/status">Go to server status</a>
			{/if}
		{:else if invalid}
			<h1>Invite unavailable</h1>
			<p class="muted">{invalid}</p>
			<a class="btn" href="/status">Server status</a>
		{:else if !info || !session.loaded}
			<p class="muted">Checking invite…</p>
		{:else if me && !info.whitelist_server_name}
			<h1>You already have an account</h1>
			<p class="muted">
				This invite creates a new account. You're signed in as <strong>{me.username}</strong>, so there's
				nothing to do — or log out to create a different account.
			</p>
			<a class="btn primary" href="/servers">Go to my servers</a>
		{:else if me}
			<div>
				<h1>You're invited</h1>
				<p class="muted sub">
					Join <strong>{info.whitelist_server_name}</strong> as <strong>{me.username}</strong>.
				</p>
			</div>
			<form class="stack" onsubmit={accept}>
				{#if error}<div class="alert error" role="alert">{error}</div>{/if}
				{#if needsMC}
					<label class="field">
						Minecraft username
						<input
							bind:value={mcUsername}
							autocapitalize="none"
							spellcheck="false"
							maxlength="16"
							required
							aria-invalid={!mcValid}
						/>
						<small>{mcValid ? 'Exactly as it appears in the game. Used for the whitelist.' : '3–16 letters, numbers or _'}</small>
					</label>
				{:else}
					<p class="muted small">You'll be whitelisted as <strong>{me.mc_username}</strong>.</p>
				{/if}
				<button class="primary" type="submit" disabled={busy || !mcValid}>{busy ? 'Joining…' : `Join ${info.whitelist_server_name}`}</button>
			</form>
			<p class="muted small center">Not {me.username}? Log out first, then open this link again.</p>
		{:else}
			<div>
				<h1>You're invited</h1>
				<p class="muted sub">
					{#if info.whitelist_server_name}
						Create an account to join <strong>{info.whitelist_server_name}</strong>.
					{:else}
						Create an account to see the servers.
					{/if}
				</p>
			</div>
			<form class="stack" onsubmit={submit}>
				{#if error}<div class="alert error" role="alert">{error}</div>{/if}
				<label class="field">
					Username
					<input bind:value={username} autocomplete="username" autocapitalize="none" maxlength="32" required />
				</label>
				<label class="field">
					Minecraft username {#if !info.requires_mc_username}<small>(optional)</small>{/if}
					<input
						bind:value={mcUsername}
						autocapitalize="none"
						spellcheck="false"
						maxlength="16"
						required={info.requires_mc_username}
						aria-invalid={!mcValid}
					/>
					<small>{mcValid ? 'Exactly as it appears in the game. Used for the whitelist.' : '3–16 letters, numbers or _'}</small>
				</label>
				<label class="field">
					Password
					<input type="password" bind:value={password} autocomplete="new-password" minlength="8" maxlength="72" required />
					<small>At least 8 characters.</small>
				</label>
				<label class="field">
					Confirm password
					<input type="password" bind:value={confirmPw} autocomplete="new-password" required />
				</label>
				<button class="primary" type="submit" disabled={busy || !mcValid}>{busy ? 'Creating account…' : 'Create account'}</button>
			</form>
			{#if info.whitelist_server_name}
				<p class="muted small center">
					Already have an account? <a href="/login?next={encodeURIComponent(`/join/${token}`)}">Log in</a> to join with it.
				</p>
			{/if}
		{/if}
	</div>
</main>

<style>
	main {
		min-height: 100dvh;
		display: grid;
		place-items: center;
		padding: 16px;
	}
	.box {
		width: 100%;
		max-width: 400px;
		padding: 24px;
	}
	.head {
		display: flex;
		justify-content: center;
	}
	h1 {
		text-align: center;
	}
	p {
		margin: 0;
	}
	.sub {
		text-align: center;
		margin-top: 4px;
	}
	.small {
		font-size: 0.85rem;
	}
	.center {
		text-align: center;
	}
</style>
