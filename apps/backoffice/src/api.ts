import type { ModelIcon } from '../../web/src/lib/api.ts';

export type Provider = {
	id: 'openrouter' | 'ollama';
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

export const api = {
	state: () => call<State>('GET', ''),
	vouchers: () => call<VoucherList>('GET', '/vouchers'),
	createVoucher: (code: string, percent: number, expires_at: string) =>
		call<VoucherList>('POST', '/vouchers', { code, percent, expires_at }),
	endVoucher: (id: string) => call<VoucherList>('POST', `/vouchers/${id}/end`),
	users: () => call<{ users: UserUsage[] }>('GET', '/users'),
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

export const providerName: Record<Provider['id'], string> = { openrouter: 'OpenRouter', ollama: 'Ollama' };
