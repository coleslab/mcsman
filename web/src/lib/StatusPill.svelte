<script lang="ts">
	import type { Status } from './api';

	let { status }: { status: Status } = $props();
</script>

<span class="pill {status}">
	<span class="dot"></span>{status}
</span>

<style>
	.pill {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 2px 10px 2px 8px;
		border-radius: 999px;
		font-size: 0.8rem;
		font-weight: 600;
		text-transform: capitalize;
		background: var(--surface-2);
		color: var(--text-2);
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: currentColor;
	}
	.running {
		background: var(--accent-soft);
		color: var(--accent);
	}
	.starting,
	.stopping {
		background: var(--warn-soft);
		color: var(--warn);
	}
	.starting .dot,
	.stopping .dot {
		animation: pulse 1s ease-in-out infinite;
	}
	.crashed {
		background: var(--danger-soft);
		color: var(--danger);
	}
	@keyframes pulse {
		50% {
			opacity: 0.3;
		}
	}
</style>
