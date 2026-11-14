import type { NearChallenge, NearSigned } from './near-login.ts';
export type User = { id: string; email: string; created_at: string };
export type Model = {
	id: string;
	name: string;
	// Our own blurb for the picker, when the model was rebranded.
	description?: string;
	// Prices are only sent to backoffice users.
	prompt_usd_per_mtok?: number;
	completion_usd_per_mtok?: number;
	context_tokens: number;
	max_output_tokens: number;
	vision: boolean;
	tools: boolean;
	// Served by NEAR AI from a sealed enclave (TEE).
	private_inference?: boolean;
	icon: ModelIcon;
};
export type ModelIcon = { code: string; color: string; shape: 'circle' | 'square' | 'diamond' | 'hexagon' };
export type CryptoQuote = {
	id: string;
	usd_micros: number;
	usd_tokens: number;
	amount_wei: string;
	rate_micros: number;
	expires_at: string;
	address: string;
};
export type CryptoDeposit = {
	id: string;
	asset: 'usdc' | 'eth' | 'near';
	amount_atomic: string;
	credit_micros: number;
	credit_tokens: number;
	status: 'credited' | 'ignored';
	tx_hash: string | null;
	created_at: string;
};
export type CryptoInfo = {
	enabled: boolean;
	chain_id?: number;
	address?: string;
	usdc?: string;
	confirmations?: number;
	min_micros?: number;
	min_tokens?: number;
	quote: CryptoQuote | null;
	voucher: CryptoVoucher | null;
	deposits: CryptoDeposit[];
};
export type CryptoVoucher = { code: string; percent: number; expires_at: string };
// NEAR payments go to one contract, attributed by the user's memo.
export type NearQuote = {
	id: string;
	usd_micros: number;
	usd_tokens: number;
	amount_yocto: string;
	rate_micros: number;
	expires_at: string;
};
export type NearInfo = {
	enabled: boolean;
	network?: 'mainnet' | 'testnet';
	contract?: string;
	usdc?: string;
	memo?: string;
	min_micros?: number;
	min_tokens?: number;
	quote: NearQuote | null;
	voucher: CryptoVoucher | null;
	deposits: CryptoDeposit[];
};
export type Balance = {
	credit_micros: number;
	held_micros: number;
	available_micros: number;
	spent_today_micros: number;
	daily_cap_micros: number;
	purchased_micros: number;
	used_micros: number;
	credit_tokens: number;
	held_tokens: number;
	available_tokens: number;
	spent_today_tokens: number;
	daily_cap_tokens: number;
	purchased_tokens: number;
	used_tokens: number;
};
export type LedgerEntry = {
	id: number;
	delta_micros: number;
	delta_tokens: number;
	reason: string;
	model: string | null;
	prompt_tokens: number | null;
	completion_tokens: number | null;
	created_at: string;
};
export type Chat = {
	id: string;
	title: string;
	model: string;
	offline: boolean;
	created_at: string;
	updated_at: string;
};
// cost_micros is only sent to backoffice users.
export type Usage = { prompt_tokens: number; completion_tokens: number; cost_micros?: number };
// SavedFile is a file a tool saved to the user's files.
export type SavedFile = { id: string; name: string; image?: boolean; size: number };
export type ToolStep = {
	id: string;
	tool: string;
	input: string;
	output?: string;
	done: boolean;
	ms?: number;
	files?: SavedFile[];
};
export type Tool = {
	id: string;
	name: string;
	description: string;
	leaves_server: boolean;
	default_on: boolean;
	enabled: boolean;
};
// python marks a file the model isn't shown but its file tools can open.
export type Attachment = { id: string; name: string; image?: boolean; python?: boolean };
// NEAR AI's signature over one model call behind a reply, and whether it
// checked out against the bytes the server actually sent and received.
export type InferenceReceipt = {
	message_id: string;
	seq: number;
	provider: string;
	model: string;
	completion_id: string;
	request_sha256: string;
	response_sha256: string;
	signed_text: string;
	signature: string;
	signing_address: string;
	status: 'verified' | 'mismatch' | 'unavailable';
	detail: string;
	created_at: string;
};
export type Message = {
	id: string;
	role: 'user' | 'assistant';
	content: string;
	attachments?: Attachment[];
	model?: string;
	usage?: Usage;
	partial?: boolean;
	steps?: ToolStep[];
	created_at: string;
};
export type FileItem = {
	id: string;
	name: string;
	mime: string;
	size: number;
	text: boolean;
	image: boolean;
	thumb: boolean;
	offline: boolean;
	created_at: string;
};

export class ApiError extends Error {
	constructor(
		public status: number,
		public code: string,
		message: string
	) {
		super(message);
	}
}

async function fail(res: Response): Promise<never> {
	let code = 'error';
	let message = `Request failed (${res.status})`;
	try {
		const body = await res.json();
		code = body.error ?? code;
		message = body.message ?? message;
	} catch {
		// non-JSON error body
	}
	throw new ApiError(res.status, code, message);
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
	const init: RequestInit = { method, credentials: 'same-origin', headers: {} };
	if (body instanceof FormData) {
		init.body = body;
	} else if (body !== undefined) {
		init.body = JSON.stringify(body);
		(init.headers as Record<string, string>)['Content-Type'] = 'application/json';
	}
	const res = await fetch(path, init);
	if (res.status === 401 && !path.startsWith('/api/auth/') && path !== '/api/me') {
		// Session expired or the server restarted (sessions are memory-only).
		location.assign('/login');
	}
	if (!res.ok) await fail(res);
	if (res.status === 204) return undefined as T;
	return res.json() as Promise<T>;
}

const get = <T>(p: string) => call<T>('GET', p);

export const api = {
	// single is the only model offered in single model mode, or '' when off.
	models: () => get<{ models: Model[]; default: string; single: string }>('/api/models'),

	// privileges lists what the user may open beyond their own data, e.g.
	// 'backoffice'. The API enforces each one; this only decides what to show.
	me: () => get<{ user: User; privileges: string[] }>('/api/me'),
	signup: (email: string, password: string) =>
		call<{ user: User; recovery_code: string }>('POST', '/api/auth/signup', { email, password }),
	login: (email: string, password: string) =>
		call<{ user: User }>('POST', '/api/auth/login', { email, password }),
	nearLoginStatus: () => get<{ enabled: boolean; network: 'mainnet' | 'testnet' }>('/api/auth/near'),
	nearChallenge: () => get<NearChallenge>('/api/auth/near/challenge'),
	// create makes the account; without it a 404 means there's none yet.
	nearLogin: (signed: NearSigned, nonce: string, passphrase: string, create: boolean) =>
		call<{ user: User; recovery_code?: string }>('POST', '/api/auth/near', { ...signed, nonce, passphrase, create }),
	recover: (email: string, recovery_code: string, new_password: string) =>
		call<{ user: User; recovery_code: string }>('POST', '/api/auth/recover', {
			email,
			recovery_code,
			new_password
		}),
	logout: () => call<void>('POST', '/api/auth/logout'),
	changePassword: (current_password: string, new_password: string) =>
		call<{ recovery_code: string }>('POST', '/api/auth/password', {
			current_password,
			new_password
		}),

	balance: () => get<{ balance: Balance }>('/api/billing/balance'),
	ledger: () => get<{ entries: LedgerEntry[] }>('/api/billing/ledger'),

	cryptoInfo: () => get<CryptoInfo>('/api/billing/crypto'),
	cryptoQuote: (usd_micros: number) =>
		call<{ quote: CryptoQuote }>('POST', '/api/billing/crypto/quote', { usd_micros }),
	nearInfo: () => get<NearInfo>('/api/billing/near'),
	nearQuote: (usd_micros: number) => call<{ quote: NearQuote }>('POST', '/api/billing/near/quote', { usd_micros }),
	claimVoucher: (code: string) => call<{ voucher: CryptoVoucher }>('POST', '/api/billing/voucher', { code }),
	removeVoucher: () => call<{ voucher: null }>('DELETE', '/api/billing/voucher'),

	chats: () => get<{ chats: Chat[] }>('/api/chats'),
	createChat: (model?: string) => call<{ chat: Chat }>('POST', '/api/chats', { model }),
	chat: (id: string) => get<{ chat: Chat; messages: Message[] }>(`/api/chats/${id}`),
	receipts: (chatID: string) => get<{ receipts: InferenceReceipt[] }>(`/api/chats/${chatID}/receipts`),
	updateChat: (id: string, patch: { title?: string; model?: string }) =>
		call<{ chat: Chat }>('PATCH', `/api/chats/${id}`, patch),
	deleteChat: (id: string) => call<void>('DELETE', `/api/chats/${id}`),
	setChatOffline: (id: string, enabled: boolean) =>
		call<{ chat: Chat }>('PUT', `/api/chats/${id}/offline`, { enabled }),

	files: () => get<{ files: FileItem[] }>('/api/files'),
	upload: (file: File) => {
		const fd = new FormData();
		fd.append('file', file);
		return call<{ file: FileItem }>('POST', '/api/files', fd);
	},
	deleteFile: (id: string) => call<void>('DELETE', `/api/files/${id}`),
	setFileOffline: (id: string, enabled: boolean) =>
		call<{ file: FileItem }>('PUT', `/api/files/${id}/offline`, { enabled }),
	fileURL: (id: string) => `/api/files/${id}/content`,
	thumbURL: (id: string) => `/api/files/${id}/thumbnail`,
	viewURL: (id: string) => `/api/files/${id}/view`,

	settings: () => get<Settings>('/api/settings'),
	setTool: (id: string, on: boolean) => call<Settings>('PUT', '/api/settings', { tools: { [id]: on } }),
	setAutoTitle: (on: boolean) => call<Settings>('PUT', '/api/settings', { auto_title: on })
};

export type Settings = { tools: Tool[]; auto_title: boolean };

export type StreamHandlers = {
	// model is sent when the server moved the chat, as single model mode
	// does with chats started before it was switched on.
	start?: (d: { user_message_id: string; message_id: string; title: string; model?: string; hold_micros: number }) => void;
	title?: (d: { title: string }) => void;
	delta?: (d: { text: string }) => void;
	reasoning?: (d: { text: string }) => void;
	tool?: (d: ToolStep) => void;
	done?: (d: { message: Message; user_message: Message; balance: Balance }) => void;
	error?: (d: { error: string; message: string }) => void;
};

// sendMessage posts a message and reads the SSE reply. EventSource cannot
// POST, so the stream is parsed by hand.
export async function sendMessage(
	chatID: string,
	content: string,
	fileIDs: string[],
	on: StreamHandlers,
	signal?: AbortSignal
) {
	const res = await fetch(`/api/chats/${chatID}/messages`, {
		method: 'POST',
		credentials: 'same-origin',
		headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
		body: JSON.stringify({ content, file_ids: fileIDs }),
		signal
	});
	if (!res.ok) await fail(res);
	if (!res.body) throw new ApiError(0, 'stream', 'The browser did not expose the reply stream');

	const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
	let buf = '';
	for (;;) {
		const { value, done } = await reader.read();
		if (done) break;
		buf += value;
		let i;
		while ((i = buf.indexOf('\n\n')) >= 0) {
			const frame = buf.slice(0, i);
			buf = buf.slice(i + 2);
			let event = 'message';
			let data = '';
			for (const line of frame.split('\n')) {
				if (line.startsWith('event: ')) event = line.slice(7);
				else if (line.startsWith('data: ')) data += line.slice(6);
			}
			if (!data) continue;
			const handler = on[event as keyof StreamHandlers] as ((d: unknown) => void) | undefined;
			handler?.(JSON.parse(data));
		}
	}
}

export const tokenCount = (tokens: number) =>
	tokens.toLocaleString(undefined, { maximumFractionDigits: 0 });

export const usd = (micros: number, digits = 2) => {
	const v = micros / 1_000_000;
	if (micros !== 0 && Math.abs(v) < 0.01) return `$${v.toFixed(6).replace(/0+$/, '')}`;
	return `$${v.toFixed(digits)}`;
};

export const bytes = (n: number) => {
	if (n < 1024) return `${n} B`;
	if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
	return `${(n / 1024 ** 2).toFixed(1)} MB`;
};

export const shortID = (id: string) => id.replace(/-/g, '').slice(-8).toUpperCase();
