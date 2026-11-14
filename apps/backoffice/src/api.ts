import type { ModelIcon } from '../../web/src/lib/api.ts';

export type Provider = {
	id: 'openrouter' | 'ollama' | 'nearai';
	base_url: string;
	key_set: boolean;
	key_hint: string;
	updated_at: string;
};
export type AdminModel = {
	id: string;
	provider: Provider['id'];
	upstream: string;
	name: string;
	// Our own blurb and identity instruction, not the provider's.
	description?: string;
	identity?: string;
	prompt_usd_per_mtok: number;
	completion_usd_per_mtok: number;
	context_tokens: number;
	max_output_tokens: number;
	vision: boolean;
	image_tokens?: number;
	tools: boolean;
	// NEAR AI only: served from an attested TEE.
	private_inference?: boolean;
	icon: ModelIcon;
	enabled: boolean;
	default: boolean;
	updated_at?: string;
};
// A model a provider serves, not necessarily in the catalog yet.
export type Offer = {
	upstream: string;
	name: string;
	description?: string;
	prompt_usd_per_mtok: number;
	completion_usd_per_mtok: number;
	context_tokens?: number;
	max_output_tokens?: number;
	vision: boolean;
	tools: boolean;
	zdr: boolean;
	// NEAR AI only: served from an attestable TEE rather than proxied to the vendor.
	verifiable: boolean;
};
// single is the one model offered in single model mode, or '' when it's off.
export type State = { providers: Provider[]; models: AdminModel[]; single: string };
export type UserUsage = {
	id: string;
	email: string;
	is_admin: boolean;
	created_at: string;
	available_tokens: number;
	held_tokens: number;
	purchased_tokens: number;
	used_tokens: number;
	spent_today_tokens: number;
	daily_cap_tokens: number;
	// The user's own cap; null when the server default applies, 0 for none.
	own_cap_tokens: number | null;
	prompt_tokens: number;
	completion_tokens: number;
	requests: number;
	used_micros: number;
};

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
	const res = await fetch(`/api/backoffice${path}`, {
		method,
		credentials: 'same-origin',
		headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (!res.ok) {
		let message = `Request failed (${res.status})`;
		try {
			message = (await res.json()).message ?? message;
		} catch {
			// non-JSON error body
		}
		throw new Error(message);
	}
	return res.json() as Promise<T>;
}

export type CryptoAddress = {
	user_id: string;
	email: string;
	index: number;
	account_number: number;
	address: string;
	deposits: number;
	credited_micros: number;
	credited_tokens: number;
	last_deposited_at: string | null;
	usdc_micros: number;
	eth_wei: string;
	eth_usd_micros: number;
	balance_error?: string;
};
export type CryptoOverview = {
	enabled: boolean;
	chain_id?: number;
	usdc?: string;
	path?: string;
	eth_usd_micros: number;
	addresses: CryptoAddress[];
	totals: {
		credited_micros: number;
		usdc_micros: number;
		eth_wei: string;
		eth_usd_micros: number;
		on_chain_micros: number;
	};
};

export type Voucher = {
	id: string;
	code: string;
	percent: number;
	expires_at: string;
	created_at: string;
	uses: number;
	paid_micros: number;
	bonus_micros: number;
};
export type VoucherList = { enabled: boolean; vouchers: Voucher[] };

// NEAR payments, all set here; the server environment has no NEAR settings.
export type NearSettings = {
	enabled: boolean;
	// Login with NEAR, on the same network. Needs no contract.
	login_enabled: boolean;
	network: 'mainnet' | 'testnet';
	rpc_url: string;
	contract: string;
	usdc: string;
	price_url: string;
	price_feed: string;
	max_price_age_secs: number;
	quote_ttl_secs: number;
	min_micros: number;
	dust_micros: number;
	tolerance_bps: number;
	poll_secs: number;
	updated_at?: string;
};
export type NearPayment = {
	index: number;
	memo: string;
	email: string;
	asset: 'near' | 'usdc' | 'other';
	token: string;
	sender: string;
	amount: string;
	block_height: number;
	at: string;
	// unmatched: read, but no user owns the memo or the token isn't priced.
	status: 'credited' | 'ignored' | 'pending' | 'unmatched';
	credit_micros: number;
};
export type NearOverview = {
	enabled: boolean;
	network: string;
	contract: string;
	usdc: string;
	near_usd_micros: number;
	near_yocto: string;
	storage_bytes: number;
	usdc_micros: number;
	balance_error?: string;
	logged: number;
	read: number;
	credited_micros: number;
	credited: number;
	recent: NearPayment[];
};
export type NearState = { settings: NearSettings; overview: NearOverview };
export type NearCheck = { name: string; ok: boolean; detail: string };

export const nearRPCs: Record<NearSettings['network'], Endpoint[]> = {
	mainnet: [
		{ label: 'FastNEAR (free)', url: 'https://free.rpc.fastnear.com' },
		{ label: 'NEAR (rate limited)', url: 'https://rpc.mainnet.near.org' }
	],
	testnet: [
		{ label: 'FastNEAR (free)', url: 'https://test.rpc.fastnear.com' },
		{ label: 'NEAR (rate limited)', url: 'https://rpc.testnet.near.org' }
	]
};
export const nearUSDC: Record<NearSettings['network'], string> = {
	mainnet: '17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1',
	testnet: '3e2210e1184b45b64c8a434c0a7e7b23cc04ea7eb7a6c3c32520d03d4afcb8af'
};

export const api = {
	state: () => call<State>('GET', ''),
	near: () => call<NearState>('GET', '/near'),
	putNear: (s: NearSettings) => call<NearState>('PUT', '/near/settings', s),
	// Tests the settings as given, saved or not.
	testNear: (s: NearSettings) => call<{ checks: NearCheck[] }>('POST', '/near/test', s),
	vouchers: () => call<VoucherList>('GET', '/vouchers'),
	createVoucher: (code: string, percent: number, expires_at: string) =>
		call<VoucherList>('POST', '/vouchers', { code, percent, expires_at }),
	endVoucher: (id: string) => call<VoucherList>('POST', `/vouchers/${id}/end`),
	users: () => call<{ users: UserUsage[] }>('GET', '/users'),
	// tokens > 0 gives, < 0 takes.
	adjustCredit: (id: string, tokens: number, note: string) =>
		call<{ users: UserUsage[] }>('POST', `/users/${id}/credit`, { tokens, note }),
	// null: server default; 0: no cap.
	setDailyCap: (id: string, tokens: number | null) => call<{ users: UserUsage[] }>('PUT', `/users/${id}/cap`, { tokens }),
	crypto: () => call<CryptoOverview>('GET', '/crypto'),
	// api_key: undefined keeps the saved key, '' clears it.
	putProvider: (id: string, base_url: string, api_key?: string) =>
		call<State>('PUT', `/providers/${id}`, { base_url, api_key }),
	testProvider: (id: string) => call<{ models: string[] }>('POST', `/providers/${id}/test`),
	// OpenRouter's come most used first.
	offers: (id: string) => call<{ offers: Offer[] }>('GET', `/providers/${id}/offers`),
	putModel: (m: AdminModel) => call<State>('PUT', `/models/${encodeURIComponent(m.id)}`, m),
	deleteModel: (id: string) => call<State>('DELETE', `/models/${encodeURIComponent(id)}`),
	setDefault: (model: string) => call<State>('PUT', '/default', { model }),
	// '' turns single model mode off.
	setSingle: (model: string) => call<State>('PUT', '/single', { model })
};

export const providerName: Record<Provider['id'], string> = {
	openrouter: 'OpenRouter',
	ollama: 'Ollama',
	nearai: 'NEAR AI'
};

// Known endpoints for each provider, picked from a list in its card. Any
// other URL can still be typed in as a custom one.
export type Endpoint = { label: string; url: string };
export const endpoints: Record<Provider['id'], Endpoint[]> = {
	openrouter: [{ label: 'OpenRouter', url: 'https://openrouter.ai/api/v1' }],
	ollama: [
		{ label: 'This machine', url: 'http://localhost:11434/v1' },
		{ label: 'Docker Compose service', url: 'http://ollama:11434/v1' }
	],
	nearai: [{ label: 'NEAR AI Cloud (private inference)', url: 'https://cloud-api.near.ai/v1' }]
};
