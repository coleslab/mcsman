<script lang="ts">
	import '../app.css';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { setUnauthorizedHandler } from '#lib/api.ts';
	import { session } from '#lib/session.svelte.ts';
	import { live } from '#lib/ws.svelte.ts';

	let { children } = $props();

	// Pages anyone can open without signing in ("/" redirects to /status).
	const PUBLIC = ['/login', '/status', '/join'];
	// Pages that render without the app header.
	const BARE = ['/login', '/join'];
	const path = $derived(page.url.pathname);
	const isPublic = $derived(path === '/' || PUBLIC.some((p) => path.startsWith(p)));
	const isBare = $derived(BARE.some((p) => path.startsWith(p)));

	setUnauthorizedHandler(() => {
		session.user = null;
		live.stop();
		if (!isPublic) goto(`/login?next=${encodeURIComponent(path)}`);
	});

	session.load();

	$effect(() => {
		if (!session.loaded) return;
		if (session.user) {
			live.start();
		} else {
			live.stop();
			if (!isPublic) goto(`/login?next=${encodeURIComponent(path)}`, { replaceState: true });
		}
	});

	const nav = $derived(
		session.isAdmin
			? [
					{ href: '/servers', label: 'Servers' },
					{ href: '/users', label: 'Users' },
					{ href: '/settings', label: 'Settings' },
					{ href: '/status', label: 'Status' }
				]
			: [
					{ href: '/servers', label: 'My servers' },
					{ href: '/status', label: 'Status' }
				]
	);

	async function logout() {
		live.stop();
		await session.logout();
		goto('/status');
	}
</script>

{#if !isBare && session.loaded}
	<header>
		<div class="container bar">
			<a href={session.user ? '/servers' : '/status'} class="brand">
				<img src="/favicon.svg" alt="" width="24" height="24" />
				<span>mcsman</span>
			</a>
			{#if session.user}
				<nav>
					{#each nav as n (n.href)}
						<a href={n.href} class:active={path.startsWith(n.href)}>{n.label}</a>
					{/each}
				</nav>
			{/if}
			<span class="spacer"></span>
			{#if session.user}
				{#if !live.connected}
					<span class="offline" title="Live updates disconnected; reconnecting…">offline</span>
				{/if}
				<a href="/account" class="user" class:active={path.startsWith('/account')} aria-label="Account">
					<svg class="icon" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true">
						<circle cx="12" cy="8" r="4" fill="none" stroke="currentColor" stroke-width="2" />
						<path d="M4 21c0-4 4-6 8-6s8 2 8 6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
					</svg>
					<span class="uname">{session.user.username}</span>
				</a>
				<button class="ghost small logout" onclick={logout}>Log out</button>
			{:else}
				<a class="btn small-btn" href="/login">Log in</a>
			{/if}
		</div>
	</header>
{/if}

{#if session.loaded && (session.user || isPublic)}
	{@render children()}
{/if}

<style>
	header {
		position: sticky;
		top: 0;
		z-index: 10;
		background: color-mix(in srgb, var(--bg) 85%, transparent);
		backdrop-filter: blur(8px);
		border-bottom: 1px solid var(--border);
	}
	.bar {
		display: flex;
		align-items: center;
		gap: 10px;
		padding-top: 10px;
		padding-bottom: 10px;
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 8px;
		font-weight: 700;
		text-decoration: none;
		font-size: 1.05rem;
	}
	nav {
		display: flex;
		gap: 2px;
		margin-left: 8px;
	}
	nav a,
	.user {
		padding: 6px 10px;
		border-radius: var(--radius-sm);
		text-decoration: none;
		color: var(--text-2);
		font-size: 0.9rem;
		font-weight: 550;
	}
	nav a:hover,
	.user:hover {
		color: var(--text);
	}
	nav a.active,
	.user.active {
		color: var(--text);
		background: var(--surface-2);
	}
	.user {
		display: flex;
		align-items: center;
	}
	.user .icon {
		display: none;
	}
	.small-btn {
		min-height: 34px;
		font-size: 0.9rem;
	}
	.offline {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: 999px;
		background: var(--warn-soft);
		color: var(--warn);
	}
	@media (max-width: 560px) {
		.brand span {
			display: none;
		}
		nav {
			margin-left: 0;
		}
		nav a {
			padding: 6px 7px;
			font-size: 0.85rem;
		}
		.logout {
			display: none;
		}
		.uname {
			display: none;
		}
		.user .icon {
			display: block;
		}
	}
</style>
