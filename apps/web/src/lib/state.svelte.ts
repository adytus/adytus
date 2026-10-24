import { compareDesc } from 'date-fns';
import { api, type Balance, type Chat, type Model, type User, type Attachment } from './api.ts';

// The API sends local-offset times and the browser writes UTC ones, so chats
// are ordered by the instant, not the string.
const newestFirst = (chats: Chat[]) =>
	chats.sort((a, b) => compareDesc(new Date(a.updated_at), new Date(b.updated_at)));

// One shared store for the signed-in shell. Pages mutate it after API calls
// so the rail and fare card stay current without refetching everything.
class AppState {
	user = $state<User | null>(null);
	privileges = $state<string[]>([]);
	balance = $state<Balance | null>(null);
	chats = $state<Chat[]>([]);
	models = $state<Model[]>([]);
	defaultModel = $state('');
	// Whether the Add tokens dialog is open.
	topUp = $state(false);
	// Bumped by New chat, so a click while already on a new chat starts over
	// instead of keeping its draft and attachments.
	newChat = $state(0);

	can(privilege: string) {
		return this.privileges.includes(privilege);
	}

	model(id: string) {
		return this.models.find((m) => m.id === id);
	}

	async load() {
		const [{ user, privileges }, { balance }, { chats }, models] = await Promise.all([
			api.me(),
			api.balance(),
			api.chats(),
			api.models()
		]);
		this.user = user;
		this.privileges = privileges;
		this.balance = balance;
		this.chats = newestFirst(chats);
		this.models = models.models;
		this.defaultModel = models.default;
	}

	openTopUp() {
		this.topUp = true;
	}

	closeTopUp() {
		this.topUp = false;
	}

	async refreshBalance() {
		this.balance = (await api.balance()).balance;
	}

	upsertChat(chat: Chat) {
		const rest = this.chats.filter((c) => c.id !== chat.id);
		this.chats = newestFirst([chat, ...rest]);
	}

	removeChat(id: string) {
		this.chats = this.chats.filter((c) => c.id !== id);
	}

	clear() {
		this.user = null;
		this.privileges = [];
		this.balance = null;
		this.chats = [];
		this.topUp = false;
	}
}

export const app = new AppState();

// A message typed on the blank chat screen, handed to the chat page after
// the chat is created so the send survives the navigation.
export type Pending = { chatID: string; content: string; files: Attachment[] };
export const handoff: { pending: Pending | null } = { pending: null };
