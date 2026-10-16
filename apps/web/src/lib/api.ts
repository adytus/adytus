export type Plan = {
	id: string;
	name: string;
	price_micros: number;
	credit_micros: number;
};

async function get<T>(path: string, fetchFn: typeof fetch = fetch): Promise<T> {
	const res = await fetchFn(path);
	if (!res.ok) throw new Error(`${path}: ${res.status}`);
	return res.json() as Promise<T>;
}

export const api = {
	models: (f?: typeof fetch) => get<{ models: string[] }>('/api/models', f),
	plans: (f?: typeof fetch) => get<{ plans: Plan[] }>('/api/plans', f)
};

export const usd = (micros: number) => `$${(micros / 1_000_000).toFixed(2)}`;
