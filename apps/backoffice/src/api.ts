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
export type State = { providers: Provider[]; models: AdminModel[] };

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

export const api = {
	state: () => call<State>('GET', ''),
	// api_key: undefined keeps the saved key, '' clears it.
	putProvider: (id: string, base_url: string, api_key?: string) =>
		call<State>('PUT', `/providers/${id}`, { base_url, api_key }),
	testProvider: (id: string) => call<{ models: string[] }>('POST', `/providers/${id}/test`),
	// OpenRouter's come most used first.
	offers: (id: string) => call<{ offers: Offer[] }>('GET', `/providers/${id}/offers`),
	putModel: (m: AdminModel) => call<State>('PUT', `/models/${encodeURIComponent(m.id)}`, m),
	deleteModel: (id: string) => call<State>('DELETE', `/models/${encodeURIComponent(id)}`),
	setDefault: (model: string) => call<State>('PUT', '/default', { model })
};

export const providerName: Record<Provider['id'], string> = { openrouter: 'OpenRouter', ollama: 'Ollama' };
