import { getContext, setContext } from 'svelte';
import { api, type Server, type Status } from './api';
import { live } from './ws.svelte';

const KEY = Symbol('server');

/** Shared state for the /servers/[id] pages. */
export class ServerState {
	server = $state<Server | null>(null);
	error = $state('');

	constructor(public id: string) {}

	/** Live status overrides the fetched one. */
	get status(): Status | undefined {
		return live.status[this.id] ?? this.server?.status;
	}

	get metrics() {
		return this.status === 'running' ? (live.metrics[this.id] ?? this.server?.metrics ?? null) : null;
	}

	async refresh() {
		try {
			this.server = await api.server(this.id);
			live.status[this.id] = this.server.status;
			this.error = '';
		} catch (e) {
			this.error = (e as Error).message;
		}
	}
}

export function provideServer(id: string) {
	const s = new ServerState(id);
	setContext(KEY, s);
	return s;
}

export function useServer(): ServerState {
	return getContext(KEY);
}
