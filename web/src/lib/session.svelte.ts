import { api, tokens, type User } from './api';

class Session {
	user = $state<User | null>(null);
	loaded = $state(false);

	get isAdmin() {
		return this.user?.role === 'admin';
	}

	async load() {
		if (!tokens.access && !tokens.refresh) {
			this.user = null;
		} else {
			try {
				this.user = await api.me();
			} catch {
				this.user = null;
			}
		}
		this.loaded = true;
		return this.user;
	}

	async login(username: string, password: string) {
		await api.login(username, password);
		return this.load();
	}

	async logout() {
		await api.logout().catch(() => {});
		this.user = null;
	}
}

export const session = new Session();
