// Login with NEAR: the wallet signs the server's one-time challenge (NEP-413).
// The wallet code is large, so it's only loaded when someone picks NEAR.

export type NearWalletOption = { id: string; name: string; icon: string };
export type NearChallenge = { message: string; recipient: string; nonce: string; network: 'mainnet' | 'testnet' };
export type NearSigned = { account_id: string; public_key: string; signature: string; callback_url: string };

type Selector = Awaited<ReturnType<typeof import('@near-wallet-selector/core').setupWalletSelector>>;

let selector: Promise<Selector> | null = null;
let selectorNetwork = '';

async function load(network: 'mainnet' | 'testnet'): Promise<Selector> {
	if (selector && selectorNetwork === network) return selector;
	selectorNetwork = network;
	selector = (async () => {
		// The wallet packages are written for Node too, and some of their
		// dependencies read Node's `global` and `process` as they load, so
		// those must exist before any of it is imported.
		const g = globalThis as { global?: unknown; process?: unknown; Buffer?: unknown };
		g.global ??= globalThis;
		g.process ??= {
			env: {},
			browser: true,
			version: '',
			versions: {},
			nextTick: (fn: (...a: unknown[]) => void, ...args: unknown[]) => queueMicrotask(() => fn(...args))
		};
		const { Buffer } = await import('buffer');
		(globalThis as { Buffer?: unknown }).Buffer ??= Buffer;
		const [{ setupWalletSelector }, { setupMeteorWallet }, { setupMyNearWallet }] = await Promise.all([
			import('@near-wallet-selector/core'),
			import('@near-wallet-selector/meteor-wallet'),
			import('@near-wallet-selector/my-near-wallet')
		]);
		return setupWalletSelector({ network, modules: [setupMeteorWallet(), setupMyNearWallet()] });
	})();
	return selector;
}

// wallets lists the wallets that can be used on this network.
export async function nearWallets(network: 'mainnet' | 'testnet'): Promise<NearWalletOption[]> {
	const s = await load(network);
	return s.store
		.getState()
		.modules.filter((m) => m.metadata.available !== false)
		.map((m) => ({ id: m.id, name: m.metadata.name, icon: m.metadata.iconUrl }));
}

// signChallenge asks the wallet to sign the challenge. No wallet needs to be
// connected first: each asks which account to sign with.
export async function signChallenge(walletID: string, ch: NearChallenge): Promise<NearSigned> {
	const s = await load(ch.network);
	const wallet = await s.wallet(walletID);
	const { Buffer } = await import('buffer');
	const signed = await wallet.signMessage({
		message: ch.message,
		recipient: ch.recipient,
		nonce: Buffer.from(ch.nonce, 'base64')
	});
	if (!signed || !signed.accountId || !signed.signature) {
		throw new Error('The wallet didn’t return a signature.');
	}
	return {
		account_id: signed.accountId,
		public_key: signed.publicKey,
		signature: signed.signature,
		callback_url: window.location.href
	};
}
