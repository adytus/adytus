import { api } from '#lib/api.ts';

export const load = async ({ fetch }) => {
	try {
		const [{ plans }, { models }] = await Promise.all([api.plans(fetch), api.models(fetch)]);
		return { plans, models, offline: false };
	} catch {
		return { plans: [], models: [], offline: true };
	}
};
