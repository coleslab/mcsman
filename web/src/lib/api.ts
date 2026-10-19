// Typed fetch wrapper for the mcsman API. Tokens live in localStorage; a 401
// triggers one refresh (shared between concurrent requests) and a retry.

export type Role = 'admin' | 'player';
export type Status = 'running' | 'stopped' | 'starting' | 'stopping' | 'crashed';

export interface User {
	id: string;
	username: string;
	mc_username: string | null;
	role: Role;
	created_at: number;
	last_login_at: number | null;
}

export interface Metrics {
	tps: number;
	mspt: number;
	ram_used_mb: number;
	ram_max_mb: number;
	player_count: number;
}

export interface Server {
	id: string;
	name: string;
	status: Status;
	mc_version: string;
	connection_address: string | null;
	player_count: number;
	players: string[];
	metrics: Metrics | null;
	// Admin-only fields:
	directory?: string;
	java_version?: string;
	port?: number;
	rcon_port?: number;
	max_memory_mb?: number;
	auto_restart?: boolean;
	created_at?: number;
	pid?: number;
	busy?: string;
	autostart?: boolean;
	backup_schedule?: string | null;
	backup_keep?: number | null;
	restart_schedule?: string | null;
	schedule?: {
		backup: string;
		backup_is_default: boolean;
		next_backup_at: number | null;
		next_restart_at: number | null;
		default_backup: string;
		default_keep: number;
	};
	last_backup?: { at: number; ok: boolean; tag?: string; error?: string };
}

export interface Backup {
	id: string;
	server_id: string;
	filename: string;
	size_bytes: number;
	sha256: string;
	tag: string | null;
	created_at: number;
	created_by: string | null;
}

export interface PublicServer {
	name: string;
	status: Status;
	player_count: number;
	version: string;
	connection_address: string | null;
}

export interface Invite {
	id: string;
	created_at: number;
	expires_at: number;
	whitelist_server_id: string | null;
	whitelist_server_name: string | null;
}

export interface CreatedInvite {
	id: string;
	token: string;
	url: string;
	expires_at: number;
	whitelist_server_name: string | null;
}

export interface InviteInfo {
	expires_at: number;
	whitelist_server_name: string | null;
	requires_mc_username: boolean;
}

export interface WhitelistedServer {
	name: string;
	connection_address: string | null;
	queued: boolean;
}

export interface Member {
	server_id: string;
	user_id: string;
	username: string;
	mc_username: string | null;
	role: Role;
	added_by: string | null;
	created_at: number;
}

export interface ServerEvent {
	id: string;
	type: string;
	detail: string | null; // admin only
	created_at: number;
}

/** What members see of a plugin; admins get the full Plugin. */
export interface PluginSummary {
	name: string;
	version: string;
}

export interface Plugin {
	id: string;
	server_id: string;
	name: string;
	version: string;
	source: 'modrinth' | 'hangar' | 'manual';
	source_id: string | null;
	filename: string;
	installed_at: number;
	updated_at: number;
}

export interface PluginSearchResult {
	source: 'modrinth' | 'hangar';
	id: string;
	slug: string;
	name: string;
	description: string;
	author: string;
	downloads: number;
	icon_url: string;
	page_url: string;
}

export interface PluginUpdate {
	plugin_id: string;
	current: string;
	latest: string;
}

export interface Compat {
	plugin: Plugin;
	status: 'green' | 'amber' | 'gray';
	release?: { version: string };
	error?: string;
}

export interface Preflight {
	current_version: string;
	target_version: string;
	target_build: number;
	current_java: number;
	required_java: number;
	java_change: boolean;
	plugins: Compat[];
	warnings: string[];
	blockers: string[];
	can_upgrade: boolean;
}

export interface UpgradeJob {
	from: string;
	to: string;
	state: 'running' | 'succeeded' | 'failed';
	phase: string;
	error?: string;
	backup_id?: string;
	plugins_updated: string[];
	plugin_errors: string[];
	steps: { phase: string; detail: string; ts: number }[];
	started_at: number;
	finished_at?: number;
}

export interface Webhook {
	id: string;
	name: string;
	url_masked: string;
	events: Record<string, boolean>;
	all_servers: boolean;
	server_ids: string[];
	server_names: string[];
	created_at: number;
}

export interface WorldImport {
	id: string;
	imported_at: number;
	source_name: string;
	source_mc_version: string;
	pre_import_backup_id: string | null;
}

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

const ACCESS = 'mcsman.access';
const REFRESH = 'mcsman.refresh';

function read(key: string): string | null {
	try {
		return localStorage.getItem(key);
	} catch {
		return null;
	}
}

function write(key: string, value: string | null) {
	try {
		if (value === null) localStorage.removeItem(key);
		else localStorage.setItem(key, value);
	} catch {
		/* storage unavailable: session lasts until reload */
	}
}

export const tokens = {
	get access() {
		return read(ACCESS);
	},
	get refresh() {
		return read(REFRESH);
	},
	set(access: string, refresh: string) {
		write(ACCESS, access);
		write(REFRESH, refresh);
	},
	clear() {
		write(ACCESS, null);
		write(REFRESH, null);
	}
};

let refreshing: Promise<boolean> | null = null;

async function refresh(): Promise<boolean> {
	const rt = tokens.refresh;
	if (!rt) return false;
	refreshing ??= (async () => {
		try {
			const res = await fetch('/api/auth/refresh', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ refresh_token: rt })
			});
			if (!res.ok) {
				tokens.clear();
				return false;
			}
			const t = await res.json();
			tokens.set(t.access_token, t.refresh_token);
			return true;
		} finally {
			refreshing = null;
		}
	})();
	return refreshing;
}

/** Called when the session can't be recovered; the layout redirects to login. */
export let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
	onUnauthorized = fn;
}

async function raw(method: string, path: string, body?: unknown, retry = true): Promise<Response> {
	const headers: Record<string, string> = {};
	const at = tokens.access;
	if (at) headers['Authorization'] = `Bearer ${at}`;
	if (body !== undefined) headers['Content-Type'] = 'application/json';
	const res = await fetch(path, {
		method,
		headers,
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (res.status === 401 && retry && tokens.refresh) {
		if (await refresh()) return raw(method, path, body, false);
		onUnauthorized();
	}
	return res;
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const res = await raw(method, path, body);
	if (!res.ok) {
		let msg = res.statusText;
		try {
			msg = (await res.json()).error ?? msg;
		} catch {
			/* non-JSON error body */
		}
		throw new ApiError(res.status, msg);
	}
	if (res.status === 204) return undefined as T;
	return res.json();
}

export const api = {
	async login(username: string, password: string) {
		const t = await request<{ access_token: string; refresh_token: string }>(
			'POST',
			'/api/auth/login',
			{ username, password }
		);
		tokens.set(t.access_token, t.refresh_token);
	},
	async logout() {
		try {
			await request('POST', '/api/auth/logout');
		} finally {
			tokens.clear();
		}
	},
	me: () => request<User>('GET', '/api/auth/me'),
	wsTicket: () => request<{ ticket: string }>('POST', '/api/ws-ticket'),

	servers: () => request<Server[]>('GET', '/api/servers'),
	server: (id: string) => request<Server>('GET', `/api/servers/${id}`),
	createServer: (body: Record<string, unknown>) => request<Server>('POST', '/api/servers', body),
	updateServer: (id: string, body: Record<string, unknown>) => request<Server>('PATCH', `/api/servers/${id}`, body),
	deleteServer: (id: string, confirm: string) => request('DELETE', `/api/servers/${id}`, { confirm }),
	start: (id: string) => request<Server>('POST', `/api/servers/${id}/start`),
	stop: (id: string, delay_seconds = 0) =>
		request<Server>('POST', `/api/servers/${id}/stop`, { delay_seconds }),
	restart: (id: string, delay_seconds = 0) =>
		request<Server>('POST', `/api/servers/${id}/restart`, { delay_seconds }),
	command: (id: string, command: string) =>
		request<{ output: string }>('POST', `/api/servers/${id}/command`, { command }),

	backups: (id: string) => request<Backup[]>('GET', `/api/servers/${id}/backups`),
	createBackup: (id: string, tag?: string) =>
		request('POST', `/api/servers/${id}/backups`, tag ? { tag } : {}),
	restoreBackup: (id: string, bid: string) =>
		request('POST', `/api/servers/${id}/backups/${bid}/restore`),
	deleteBackup: (id: string, bid: string) =>
		request('DELETE', `/api/servers/${id}/backups/${bid}`),
	downloadBackup: (id: string, b: Backup) => download(`/api/servers/${id}/backups/${b.id}/download`),
	exportWorld: (id: string) => download(`/api/servers/${id}/worlds/export`),
	worldImports: (id: string) => request<WorldImport[]>('GET', `/api/servers/${id}/worlds/imports`),

	webhooks: () => request<{ webhooks: Webhook[]; event_keys: string[] }>('GET', '/api/webhooks'),
	createWebhook: (name: string, url: string, server_ids: string[] = []) =>
		request<Webhook>('POST', '/api/webhooks', { name, url, server_ids }),
	updateWebhook: (
		id: string,
		body: { name?: string; url?: string; events?: Record<string, boolean>; server_ids?: string[] }
	) =>
		request<Webhook>('PATCH', `/api/webhooks/${id}`, body),
	deleteWebhook: (id: string) => request('DELETE', `/api/webhooks/${id}`),
	testWebhook: (id: string) => request('POST', `/api/webhooks/${id}/test`),

	status: () => request<{ servers: PublicServer[] }>('GET', '/api/status'),

	invites: () => request<Invite[]>('GET', '/api/invites'),
	createInvite: (expires_in: string, whitelist_server_id?: string) =>
		request<CreatedInvite>('POST', '/api/invites', { expires_in, whitelist_server_id }),
	revokeInvite: (id: string) => request('DELETE', `/api/invites/${id}`),
	inviteInfo: (token: string) => request<InviteInfo>('GET', `/api/invites/${encodeURIComponent(token)}`),
	async redeem(token: string, body: { username: string; password: string; mc_username?: string }) {
		const r = await request<{
			access_token: string;
			refresh_token: string;
			whitelisted_server: WhitelistedServer | null;
		}>('POST', `/api/invites/${encodeURIComponent(token)}/redeem`, body);
		tokens.set(r.access_token, r.refresh_token);
		return r.whitelisted_server;
	},
	acceptInvite: async (token: string, mc_username?: string) =>
		(
			await request<{ whitelisted_server: WhitelistedServer | null }>(
				'POST',
				`/api/invites/${encodeURIComponent(token)}/accept`,
				mc_username ? { mc_username } : {}
			)
		).whitelisted_server,

	users: () => request<User[]>('GET', '/api/users'),
	setRole: (id: string, role: Role) => request<User>('PATCH', `/api/users/${id}`, { role }),
	setUserMC: (id: string, mc_username: string) => request<User>('PATCH', `/api/users/${id}`, { mc_username }),
	deleteUser: (id: string) => request('DELETE', `/api/users/${id}`),
	changePassword: (current: string, next: string) =>
		request('POST', '/api/users/me/password', { current, new: next }),
	setMyMC: (mc_username: string) => request<User>('PATCH', '/api/users/me', { mc_username }),

	members: (id: string) => request<Member[]>('GET', `/api/servers/${id}/members`),
	addMember: (id: string, user_id: string) =>
		request<{ user_id: string; username: string; queued: boolean }>('POST', `/api/servers/${id}/members`, { user_id }),
	removeMember: (id: string, user_id: string) => request('DELETE', `/api/servers/${id}/members/${user_id}`),
	serverEvents: (id: string) => request<ServerEvent[]>('GET', `/api/servers/${id}/events`),

	plugins: (id: string) => request<Plugin[]>('GET', `/api/servers/${id}/plugins`),
	pluginSummaries: (id: string) => request<PluginSummary[]>('GET', `/api/servers/${id}/plugins`),
	searchPlugins: (q: string, mc_version: string) =>
		request<{ results: PluginSearchResult[]; errors: Record<string, string> }>(
			'GET',
			`/api/plugins/search?${new URLSearchParams({ q, mc_version })}`
		),
	installPlugin: (id: string, source: string, source_id: string) =>
		request<Plugin>('POST', `/api/servers/${id}/plugins`, { source, source_id }),
	updatePlugin: (id: string, pid: string) =>
		request<{ plugin: Plugin; updated: boolean }>('POST', `/api/servers/${id}/plugins/${pid}/update`),
	removePlugin: (id: string, pid: string) => request('DELETE', `/api/servers/${id}/plugins/${pid}`),
	pluginUpdates: (id: string) => request<PluginUpdate[]>('GET', `/api/servers/${id}/plugins/updates`),

	preflight: (id: string, target_version: string) =>
		request<Preflight>('POST', `/api/servers/${id}/upgrade`, { target_version }),
	confirmUpgrade: (id: string, target_version: string, delay_seconds?: number) =>
		request<UpgradeJob>('POST', `/api/servers/${id}/upgrade/confirm`, { target_version, delay_seconds }),
	upgradeStatus: (id: string) => request<{ job: UpgradeJob | null }>('GET', `/api/servers/${id}/upgrade/status`),

	paperVersions: (refresh = false) =>
		request<{ versions: string[] }>('GET', `/api/versions/paper${refresh ? '?refresh=true' : ''}`)
};

/**
 * Starts a streamed download: a single-use ticket authorises a plain
 * navigation, so large files go straight to disk instead of memory.
 */
async function download(path: string) {
	const { ticket } = await api.wsTicket();
	const a = document.createElement('a');
	a.href = `${path}?ticket=${encodeURIComponent(ticket)}`;
	a.rel = 'noopener';
	document.body.append(a);
	a.click();
	a.remove();
}

/** Uploads a world zip with progress reporting (fetch can't report upload progress). */
export async function importWorld(
	id: string,
	file: File,
	opts: { sourceName?: string; force?: boolean },
	onProgress: (fraction: number) => void
): Promise<WorldImport> {
	// Make sure the access token is fresh before a long upload.
	await api.me();
	const form = new FormData();
	if (opts.sourceName) form.append('source_name', opts.sourceName);
	if (opts.force) form.append('force', 'true');
	form.append('world', file);
	return new Promise((resolve, reject) => {
		const xhr = new XMLHttpRequest();
		xhr.open('POST', `/api/servers/${id}/worlds/import`);
		const at = tokens.access;
		if (at) xhr.setRequestHeader('Authorization', `Bearer ${at}`);
		xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
		xhr.onload = () => {
			let body: { error?: string; inspection?: unknown } & Partial<WorldImport> = {};
			try {
				body = JSON.parse(xhr.responseText);
			} catch {
				/* non-JSON */
			}
			if (xhr.status === 201) resolve(body as WorldImport);
			else reject(Object.assign(new ApiError(xhr.status, body.error ?? xhr.statusText), { inspection: body.inspection }));
		};
		xhr.onerror = () => reject(new ApiError(0, 'Upload failed: network error'));
		xhr.send(form);
	});
}

/** Opens an authenticated WebSocket using a single-use ticket. */
export async function openSocket(path: string): Promise<WebSocket> {
	const { ticket } = await api.wsTicket();
	const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
	return new WebSocket(`${proto}//${location.host}${path}?ticket=${encodeURIComponent(ticket)}`);
}
