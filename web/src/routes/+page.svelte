<script lang="ts">
	import { goto } from '$app/navigation';
	import { session } from '#lib/session.svelte.ts';

	// The root layout only renders this once the session has loaded.
	goto(session.user ? '/servers' : '/status', { replaceState: true });
</script>
