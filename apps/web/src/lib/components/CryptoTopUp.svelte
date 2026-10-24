<script lang="ts">
	import qrcode from 'qrcode-generator';
	import { api, ApiError, tokenCount, usd, type CryptoInfo, type CryptoQuote } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import Alert from '#lib/components/Alert.svelte';

	let { onpaid }: { onpaid?: () => void } = $props();

	let info = $state<CryptoInfo | null>(null);
	let asset = $state<'usdc' | 'eth'>('usdc');
	let amount = $state('10');
	let quote = $state<CryptoQuote | null>(null);
	let busy = $state(false);
	let error = $state('');
	let received = $state('');
	let copied = $state('');
	let now = $state(Date.now());

	// Deposits seen at the last poll, to spot a new credit.
	let known: Set<string> | null = null;

	async function load() {
		try {
			const r = await api.cryptoInfo();
			if (known && r.deposits.some((d) => d.status === 'credited' && !known!.has(d.id))) {
				const fresh = r.deposits.filter((d) => d.status === 'credited' && !known!.has(d.id));
				const total = fresh.reduce((n, d) => n + d.credit_tokens, 0);
				received = `${tokenCount(total)} tokens were added from your ${fresh.map((d) => d.asset.toUpperCase()).join(' and ')} payment.`;
				await app.refreshBalance();
				onpaid?.();
			}
			known = new Set(r.deposits.map((d) => d.id));
			info = r;
			// An ETH quote that is still open belongs on screen across reloads.
			if (!quote && r.quote) quote = r.quote;
		} catch {
			// A failed poll leaves the last good state on screen.
		}
	}

	$effect(() => {
		load();
		const poll = setInterval(() => {
			if (!document.hidden) load();
		}, 10_000);
		const tick = setInterval(() => (now = Date.now()), 1000);
		return () => {
			clearInterval(poll);
			clearInterval(tick);
		};
	});

	const qrSvg = (text: string) => {
		const q = qrcode(0, 'M');
		q.addData(text);
		q.make();
		return q.createSvgTag({ cellSize: 4, margin: 2, scalable: true });
	};

	const WEI = 10n ** 18n;
	// Rounded up to 6 decimals so the amount shown is never short of the quote.
	const ethText = (wei: string) => {
		const step = 10n ** 12n;
		const w = (BigInt(wei) + step - 1n) / step;
		const whole = w / 1_000_000n;
		const frac = (w % 1_000_000n).toString().padStart(6, '0');
		return `${whole}.${frac}`;
	};
	const ethExact = (wei: string) => {
		const w = BigInt(wei);
		return `${w / WEI}.${(w % WEI).toString().padStart(18, '0').replace(/0+$/, '') || '0'}`;
	};

	const left = $derived(quote ? Math.max(0, Math.floor((new Date(quote.expires_at).getTime() - now) / 1000)) : 0);
	const live = $derived(quote !== null && left > 0);
	const mmss = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;

	const minUSD = $derived((info?.min_micros ?? 0) / 1_000_000);
	const micros = $derived(Math.round(Number(amount) * 1_000_000));
	const validAmount = $derived(Number.isFinite(micros) && micros >= (info?.min_micros ?? 0) && micros <= 10_000 * 1_000_000);

	async function getQuote(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			quote = (await api.cryptoQuote(micros)).quote;
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Could not get a price. Try again.';
		} finally {
			busy = false;
		}
	}

	async function copy(label: string, text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copied = label;
			setTimeout(() => (copied = ''), 2000);
		} catch {
			// Clipboard blocked; the text is selectable on screen.
		}
	}

	const explorer = (chain?: number) => (chain === 84532 ? 'https://sepolia.basescan.org' : 'https://basescan.org');
	const fmtDate = (s: string) =>
		new Date(s).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
	const amountText = (d: { asset: string; amount_atomic: string }) =>
		d.asset === 'usdc' ? `${(Number(d.amount_atomic) / 1e6).toLocaleString(undefined, { maximumFractionDigits: 6 })} USDC` : `${ethText(d.amount_atomic)} ETH`;
</script>

{#if info?.enabled && info.address}
	<section aria-labelledby="crypto-h">
		<h2 id="crypto-h">Pay with crypto</h2>
		<p class="muted">
			Send USDC or ETH on <strong>Base</strong> to your personal address. Tokens are added after
			{info.confirmations} confirmations, about {Math.round((info.confirmations ?? 12) * 2)} seconds. The
			address is yours alone and works for any amount over {usd(info.min_micros ?? 0, 0)}; tokens are
			credited at $1 per USDC, or at the ETH price when you pay.
		</p>

		<div class="assets" role="radiogroup" aria-label="Currency">
			<button type="button" role="radio" aria-checked={asset === 'usdc'} class:on={asset === 'usdc'} onclick={() => (asset = 'usdc')}>USDC</button>
			<button type="button" role="radio" aria-checked={asset === 'eth'} class:on={asset === 'eth'} onclick={() => (asset = 'eth')}>ETH</button>
		</div>

		{#if asset === 'usdc'}
			<div class="pay">
				<div class="qr" aria-hidden="true">{@html qrSvg(info.address)}</div>
				<div class="how">
					<p class="micro">Send USDC on Base to</p>
					<p class="addr">{info.address}</p>
					<button class="btn" type="button" onclick={() => copy('addr', info!.address!)}>
						{copied === 'addr' ? 'Copied' : 'Copy address'}
					</button>
					<p class="muted small">
						Only USDC on the Base network. Other networks or tokens sent here cannot be
						recovered automatically.
					</p>
				</div>
			</div>
		{:else if live && quote}
			<div class="pay">
				<div class="qr" aria-hidden="true">
					{@html qrSvg(`ethereum:${quote.address}@${info.chain_id}?value=${quote.amount_wei}`)}
				</div>
				<div class="how">
					<p class="micro">Send this much ETH on Base to</p>
					<p class="amt">{ethText(quote.amount_wei)} ETH</p>
					<p class="addr">{quote.address}</p>
					<div class="row">
						<button class="btn" type="button" onclick={() => copy('addr', quote!.address)}>
							{copied === 'addr' ? 'Copied' : 'Copy address'}
						</button>
						<button class="btn" type="button" onclick={() => copy('amt', ethExact(quote!.amount_wei))}>
							{copied === 'amt' ? 'Copied' : 'Copy amount'}
						</button>
					</div>
					<p class="muted small">
						Worth {usd(quote.usd_micros, 2)} ({tokenCount(quote.usd_tokens)} tokens) at {usd(quote.rate_micros, 2)} per ETH.
						Price held for <strong>{mmss(left)}</strong>; paying later is credited at the price
						then.
					</p>
				</div>
			</div>
		{:else}
			<form class="quote" onsubmit={getQuote}>
				<label class="field">
					Amount in USD
					<input type="number" inputmode="decimal" min={minUSD} max="10000" step="any" required bind:value={amount} />
				</label>
				<button class="btn primary" disabled={busy || !validAmount}>{busy ? 'Pricing…' : 'Get ETH price'}</button>
				<p class="muted small">Minimum {usd(info.min_micros ?? 0, 0)}. We lock the ETH price for 15 minutes.</p>
			</form>
		{/if}

		{#if asset === 'eth' && live}
			<button class="link" type="button" onclick={() => (quote = null)}>Start over with a different amount</button>
		{/if}

		{#if error}<Alert title="No price yet" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}
		{#if received}<Alert tone="ok" title="Payment received" ondismiss={() => (received = '')}><p>{received}</p></Alert>{/if}

		{#if info.deposits.length > 0}
			<h3>Recent crypto payments</h3>
			<ul class="deposits">
				{#each info.deposits as d (d.id)}
					<li>
						<span>{fmtDate(d.created_at)}</span>
						<span>{amountText(d)}</span>
						<span class:plus={d.status === 'credited'}>
							{d.status === 'credited' ? `+${tokenCount(d.credit_tokens)} tokens` : 'too small to credit'}
						</span>
						{#if d.tx_hash}
							<a href="{explorer(info.chain_id)}/tx/{d.tx_hash}" target="_blank" rel="noopener noreferrer">View</a>
						{:else}
							<span></span>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{/if}

<style>
	h2 {
		margin: 0 0 0.5rem;
		font-size: 1.375rem;
		font-weight: 800;
		font-stretch: 85%;
	}
	h3 {
		margin: 1.5rem 0 0.5rem;
		font-size: 1rem;
		font-weight: 700;
	}
	.muted {
		color: var(--muted);
		margin: 0.25rem 0 1rem;
		line-height: 1.5;
		max-width: 62ch;
	}
	.small {
		font-size: 0.8125rem;
		margin-bottom: 0;
	}
	.assets {
		display: inline-flex;
		border: 2px solid var(--rule);
		border-radius: 0.5rem;
		overflow: hidden;
		margin-bottom: 1rem;
	}
	.assets button {
		padding: 0.5rem 1.25rem;
		font-weight: 700;
		background: transparent;
		color: var(--text-soft);
		border: 0;
		cursor: pointer;
	}
	.assets button.on {
		background: var(--surface-2);
		color: var(--paper);
	}
	.pay {
		display: flex;
		flex-wrap: wrap;
		gap: 1.25rem;
		align-items: flex-start;
	}
	.qr {
		width: 10.5rem;
		background: #fff;
		border-radius: 0.5rem;
		padding: 0.25rem;
		flex: none;
	}
	.qr :global(svg) {
		display: block;
		width: 100%;
		height: auto;
	}
	.how {
		flex: 1 1 18rem;
		min-width: 0;
	}
	.how p {
		margin: 0 0 0.5rem;
	}
	.addr {
		font-family: ui-monospace, monospace;
		font-size: 0.875rem;
		overflow-wrap: anywhere;
		user-select: all;
	}
	.amt {
		font-size: 1.75rem;
		font-weight: 900;
		font-stretch: 75%;
		font-variant-numeric: tabular-nums;
	}
	.row {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.quote {
		display: grid;
		gap: 0.875rem;
		max-width: 16rem;
	}
	.quote .btn {
		justify-self: start;
	}
	.link {
		background: none;
		border: 0;
		padding: 0;
		margin-top: 0.75rem;
		color: var(--text-soft);
		text-decoration: underline;
		cursor: pointer;
	}
	.deposits {
		list-style: none;
		margin: 0;
		padding: 0;
		font-size: 0.875rem;
		font-variant-numeric: tabular-nums;
	}
	.deposits li {
		display: grid;
		grid-template-columns: 8rem 1fr 1fr 3rem;
		gap: 0.75rem;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--rule);
	}
	.plus {
		color: var(--line-green);
	}
	@media (max-width: 560px) {
		.deposits li {
			grid-template-columns: 1fr 1fr;
		}
	}
</style>
