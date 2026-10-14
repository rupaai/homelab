import { api } from './api';
import { messageOf } from './errors';
import type { AppView } from './types';

/** The apps overview, shared by every screen, refreshed by polling. */
class AppsStore {
	list = $state<AppView[]>([]);
	loaded = $state(false);
	error = $state('');

	get anyBusy(): boolean {
		return this.list.some((a) => a.envs.some((e) => e.busy));
	}
	/** How long to wait between refreshes: quicker while something is changing. */
	get pollMs(): number {
		return this.anyBusy ? 2000 : 5000;
	}
	get = (id: string): AppView | undefined => this.list.find((a) => a.id === id);

	refresh = async (): Promise<void> => {
		try {
			this.list = await api.apps();
			this.error = '';
		} catch (e) {
			this.error = messageOf(e);
		} finally {
			this.loaded = true;
		}
	};
}

export const appsStore = new AppsStore();
