import { mount as mountApp, unmount } from 'svelte';
import Backoffice from './Backoffice.svelte';

// mount renders the backoffice into target and returns its teardown. The web
// app calls it after importing this module from the API.
export function mount(target: HTMLElement): () => void {
	const app = mountApp(Backoffice, { target });
	return () => void unmount(app);
}
