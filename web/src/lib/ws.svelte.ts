// Live event feed (/api/events): server status, metrics, players and, for
// admins, backup progress. Reconnects with backoff.
import { openSocket, type Metrics, type Status } from './api';

export interface WsEvent<T = unknown> {
	type: string;
	server_id: string;
	ts: number;
	data: T;
}

export interface BackupProgress {
	phase: 'flush' | 'archive' | 'verify' | 'done';
	bytes_written: number;
}

class Live {
	status = $state<Record<string, Status>>({});
	metrics = $state<Record<string, Metrics>>({});
	backup = $state<Record<string, BackupProgress | null>>({});
	connected = $state(false);

	private socket: WebSocket | null = null;
	private listeners = new Set<(e: WsEvent) => void>();
	private retry = 0;
	private stopped = true;

	start() {
		if (!this.stopped) return;
		this.stopped = false;
		this.connect();
	}

	stop() {
		this.stopped = true;
		this.socket?.close();
		this.socket = null;
	}

	/** Subscribe to raw events; returns an unsubscribe function. */
	on(fn: (e: WsEvent) => void) {
		this.listeners.add(fn);
		return () => this.listeners.delete(fn);
	}

	private async connect() {
		if (this.stopped) return;
		try {
			const ws = await openSocket('/api/events');
			this.socket = ws;
			ws.onopen = () => {
				this.connected = true;
				this.retry = 0;
			};
			ws.onmessage = (m) => this.handle(JSON.parse(m.data));
			ws.onclose = () => {
				this.connected = false;
				this.socket = null;
				this.schedule();
			};
		} catch {
			this.schedule();
		}
	}

	private schedule() {
		if (this.stopped) return;
		const delay = Math.min(30_000, 1000 * 2 ** this.retry++);
		setTimeout(() => this.connect(), delay);
	}

	private handle(e: WsEvent) {
		switch (e.type) {
			case 'server_status': {
				const s = (e.data as { status: Status }).status;
				this.status[e.server_id] = s;
				if (s !== 'running') delete this.metrics[e.server_id];
				break;
			}
			case 'metrics':
				this.metrics[e.server_id] = e.data as Metrics;
				break;
			case 'backup_progress': {
				const p = e.data as BackupProgress;
				this.backup[e.server_id] = p.phase === 'done' ? null : p;
				break;
			}
			case 'backup_failed':
				this.backup[e.server_id] = null;
				break;
		}
		for (const fn of this.listeners) fn(e);
	}
}

export const live = new Live();
