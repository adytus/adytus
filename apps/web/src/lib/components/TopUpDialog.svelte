<script lang="ts">
	import qrcode from 'qrcode-generator';
	import { api, ApiError, tokenCount, usd, type CryptoInfo, type CryptoQuote } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import Alert from '#lib/components/Alert.svelte';

	// Add tokens: pick an amount and a currency, then pay on Base. The page
	// watches for the payment and closes the loop when it is credited.
	const PRESETS = [10, 25, 50, 100];
	const MAX_USD = 10_000;

	let dialog = $state<HTMLDialogElement>();
	let info = $state<CryptoInfo | null>(null);
	let loadError = $state(false);

	let stage = $state<'choose' | 'pay' | 'done'>('choose');
	let preset = $state<number | 'custom'>(25);
	let custom = $state('');
	let asset = $state<'usdc' | 'eth'>('usdc');
	let quote = $state<CryptoQuote | null>(null);
	let busy = $state(false);
	let error = $state('');
	let copied = $state('');
	let added = $state(0);
	let now = $state(Date.now());

	// Deposits seen at the last poll, to spot a new credit.
	let known: Set<string> | null = null;

	const dollars = $derived(preset === 'custom' ? Number(custom) : preset);
	const micros = $derived(Math.round(dollars * 1_000_000));
	const minMicros = $derived(info?.min_micros ?? 0);
	const validAmount = $derived(Number.isFinite(micros) && micros >= minMicros && micros <= MAX_USD * 1_000_000);
	const tokens = $derived(validAmount ? Math.floor(micros / 1000) : 0);

	$effect(() => {
		if (!dialog) return;
		if (app.topUp && !dialog.open) {
			reset();
			dialog.showModal();
		} else if (!app.topUp && dialog.open) {
			dialog.close();
		}
	});

	function reset() {
		stage = 'choose';
		quote = null;
		error = '';
		added = 0;
		known = null;
		loadError = false;
	}

	async function load() {
		try {
			const r = await api.cryptoInfo();
			const fresh = known ? r.deposits.filter((d) => d.status === 'credited' && !known!.has(d.id)) : [];
			known = new Set(r.deposits.map((d) => d.id));
			info = r;
			loadError = false;
			if (fresh.length > 0) {
				added = fresh.reduce((n, d) => n + d.credit_tokens, 0);
				stage = 'done';
				await app.refreshBalance();
			}
		} catch {
			// A failed poll keeps the last good state; only a first load shows the error.
			if (!info) loadError = true;
		}
	}

	// Poll only while the dialog is open.
	$effect(() => {
		if (!app.topUp) return;
		load();
		const poll = setInterval(() => {
			if (!document.hidden) load();
		}, 8_000);
		const tick = setInterval(() => (now = Date.now()), 1000);
		return () => {
			clearInterval(poll);
			clearInterval(tick);
		};
	});

	async function next(e?: SubmitEvent) {
		e?.preventDefault();
		if (!validAmount || !info) return;
		error = '';
		if (asset === 'usdc') {
			stage = 'pay';
			return;
		}
		await getQuote();
	}

	async function getQuote() {
		busy = true;
		error = '';
		try {
			quote = (await api.cryptoQuote(micros)).quote;
			stage = 'pay';
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Could not get a price. Try again.';
		} finally {
			busy = false;
		}
	}

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
		return `${w / 1_000_000n}.${(w % 1_000_000n).toString().padStart(6, '0')}`;
	};
	const ethExact = (wei: string) => {
		const w = BigInt(wei);
		return `${w / WEI}.${(w % WEI).toString().padStart(18, '0').replace(/0+$/, '') || '0'}`;
	};

	const left = $derived(quote ? Math.max(0, Math.floor((new Date(quote.expires_at).getTime() - now) / 1000)) : 0);
	const mmss = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
	const expired = $derived(asset === 'eth' && stage === 'pay' && left === 0);

	async function copy(label: string, text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copied = label;
			setTimeout(() => (copied = ''), 1800);
		} catch {
			// Clipboard blocked; the text is selectable on screen.
		}
	}

	const explorer = (chain?: number) => (chain === 84532 ? 'https://sepolia.basescan.org' : 'https://basescan.org');
	const fmtDate = (s: string) =>
		new Date(s).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
	const amountText = (d: { asset: string; amount_atomic: string }) =>
		d.asset === 'usdc'
			? `${(Number(d.amount_atomic) / 1e6).toLocaleString(undefined, { maximumFractionDigits: 6 })} USDC`
			: `${ethText(d.amount_atomic)} ETH`;

	function onBackdrop(e: MouseEvent) {
		if (e.target === dialog) app.closeTopUp();
	}
</script>

<dialog bind:this={dialog} aria-labelledby="topup-h" onclose={() => app.closeTopUp()} onclick={onBackdrop}>
	<div class="sheet">
		<header>
			<div>
				<h2 id="topup-h">Add tokens</h2>
				<p class="sub">Pay with USDC or ETH on Base. $1 buys 1,000 tokens.</p>
			</div>
			<button class="x" type="button" aria-label="Close" onclick={() => app.closeTopUp()}>×</button>
		</header>

		{#if loadError}
			<Alert title="Payments are unavailable">
				<p>The server didn't answer. Close this and try again in a moment.</p>
			</Alert>
		{:else if !info}
			<p class="state">Loading…</p>
		{:else if !info.enabled}
			<Alert tone="info" title="Crypto payments aren't set up yet">
				<p>This server has no payment address configured, so tokens can't be added right now.</p>
			</Alert>
		{:else if stage === 'done'}
			<div class="done" role="status">
				<span class="check" aria-hidden="true">✓</span>
				<p class="big">{tokenCount(added)} tokens added</p>
				<p class="sub">You have {tokenCount(app.balance?.available_tokens ?? 0)} tokens left.</p>
				<div class="actions">
					<button class="btn primary" type="button" onclick={() => app.closeTopUp()}>Done</button>
					<button class="btn" type="button" onclick={reset}>Add more</button>
				</div>
			</div>
		{:else if stage === 'choose'}
			<form class="choose" onsubmit={next}>
				<fieldset>
					<legend>Amount</legend>
					<div class="chips">
						{#each PRESETS as p (p)}
							<button type="button" class="chip" class:on={preset === p} aria-pressed={preset === p} onclick={() => (preset = p)}>
								${p}
							</button>
						{/each}
						<button type="button" class="chip" class:on={preset === 'custom'} aria-pressed={preset === 'custom'} onclick={() => (preset = 'custom')}>
							Other
						</button>
					</div>
					{#if preset === 'custom'}
						<label class="field">
							<span class="sr-only">Amount in US dollars</span>
							<span class="money">
								<span aria-hidden="true">$</span>
								<input type="number" inputmode="decimal" min={minMicros / 1_000_000} max={MAX_USD} step="any" placeholder="Amount in USD" bind:value={custom} />
							</span>
						</label>
					{/if}
					<p class="total">
						{#if validAmount}
							<strong>{tokenCount(tokens)}</strong> tokens
						{:else}
							Minimum {usd(minMicros, 0)}
						{/if}
					</p>
				</fieldset>

				<fieldset>
					<legend>Pay with</legend>
					<div class="seg" role="radiogroup" aria-label="Currency">
						<button type="button" role="radio" aria-checked={asset === 'usdc'} class:on={asset === 'usdc'} onclick={() => (asset = 'usdc')}>
							<strong>USDC</strong><span>Stable, $1 each</span>
						</button>
						<button type="button" role="radio" aria-checked={asset === 'eth'} class:on={asset === 'eth'} onclick={() => (asset = 'eth')}>
							<strong>ETH</strong><span>Price locked 15 min</span>
						</button>
					</div>
				</fieldset>

				{#if error}<Alert title="No price yet" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}
				<button class="btn primary wide" disabled={!validAmount || busy}>
					{busy ? 'Getting price…' : 'Continue'}
				</button>
			</form>
		{:else}
			<div class="pay">
				<div class="summary">
					<span>{tokenCount(tokens)} tokens</span>
					<button type="button" class="link" onclick={() => (stage = 'choose')}>Change</button>
				</div>

				{#if asset === 'usdc'}
					<p class="step">Send <strong>{dollars.toLocaleString(undefined, { maximumFractionDigits: 2 })} USDC</strong> on the <strong>Base</strong> network to:</p>
					<div class="card">
						<div class="qr" aria-hidden="true">{@html qrSvg(info.address!)}</div>
						<div class="where">
							<p class="addr">{info.address}</p>
							<button class="btn" type="button" onclick={() => copy('addr', info!.address!)}>
								{copied === 'addr' ? 'Copied' : 'Copy address'}
							</button>
						</div>
					</div>
					<p class="note">Any amount over {usd(minMicros, 0)} works, so it needn't match exactly. Other tokens or networks sent here can't be recovered automatically.</p>
				{:else if quote}
					<p class="step">Send <strong>{ethText(quote.amount_wei)} ETH</strong> on the <strong>Base</strong> network to:</p>
					<div class="card" class:stale={expired}>
						<div class="qr" aria-hidden="true">
							{@html qrSvg(`ethereum:${quote.address}@${info.chain_id}?value=${quote.amount_wei}`)}
						</div>
						<div class="where">
							<p class="addr">{quote.address}</p>
							<div class="row">
								<button class="btn" type="button" onclick={() => copy('addr', quote!.address)}>
									{copied === 'addr' ? 'Copied' : 'Copy address'}
								</button>
								<button class="btn" type="button" onclick={() => copy('amt', ethExact(quote!.amount_wei))}>
									{copied === 'amt' ? 'Copied' : 'Copy amount'}
								</button>
							</div>
						</div>
					</div>
					{#if expired}
						<Alert tone="info" title="This price has expired">
							<p>A payment sent now is credited at the current ETH price instead.</p>
							{#snippet action()}<button class="btn" type="button" disabled={busy} onclick={getQuote}>New price</button>{/snippet}
						</Alert>
					{:else}
						<p class="note">
							{usd(quote.rate_micros, 2)} per ETH, held for <strong>{mmss(left)}</strong>.
						</p>
					{/if}
				{/if}
				{#if error}<Alert title="No price yet" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}

				<p class="wait" role="status"><span class="pulse" aria-hidden="true"></span>Waiting for your payment. Tokens appear after {info.confirmations} confirmations, about {Math.round((info.confirmations ?? 12) * 2)} seconds.</p>
			</div>
		{/if}

		{#if info?.enabled && info.deposits.length > 0 && stage !== 'done'}
			<details class="history">
				<summary>Recent payments</summary>
				<ul>
					{#each info.deposits as d (d.id)}
						<li>
							<span>{fmtDate(d.created_at)}</span>
							<span>{amountText(d)}</span>
							<span class:plus={d.status === 'credited'}>
								{d.status === 'credited' ? `+${tokenCount(d.credit_tokens)}` : 'too small'}
							</span>
							{#if d.tx_hash}
								<a href="{explorer(info.chain_id)}/tx/{d.tx_hash}" target="_blank" rel="noopener noreferrer">View</a>
							{:else}
								<span></span>
							{/if}
						</li>
					{/each}
				</ul>
			</details>
		{/if}
	</div>
</dialog>

<style>
	dialog {
		margin: auto;
		padding: 0;
		border: 1px solid var(--rule);
		border-radius: 20px;
		background: var(--stock);
		color: var(--ink);
		width: min(30rem, calc(100vw - 1.5rem));
		max-height: calc(100dvh - 2rem);
		overflow: auto;
		box-shadow: 0 24px 64px rgb(28 28 28 / 0.25);
	}
	dialog::backdrop {
		background: rgb(28 28 28 / 0.45);
	}
	.sheet {
		display: grid;
		gap: 1.25rem;
		padding: 1.5rem;
	}
	header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
	}
	h2 {
		margin: 0;
		font-size: 1.5rem;
		font-weight: 700;
	}
	.sub {
		margin: 0.25rem 0 0;
		color: var(--muted);
		font-size: 0.9375rem;
	}
	.x {
		flex: none;
		width: 2rem;
		height: 2rem;
		border: 0;
		border-radius: 50%;
		background: transparent;
		color: var(--muted);
		font-size: 1.5rem;
		line-height: 1;
		cursor: pointer;
	}
	.x:hover {
		background: var(--surface-2);
		color: var(--ink);
	}
	.state {
		color: var(--muted);
		margin: 0;
	}
	fieldset {
		border: 0;
		margin: 0;
		padding: 0;
		min-width: 0;
		display: grid;
		gap: 0.625rem;
	}
	legend {
		padding: 0;
		margin-bottom: 0.5rem;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--muted);
		letter-spacing: 0.04em;
		text-transform: uppercase;
	}
	.choose {
		display: grid;
		gap: 1.25rem;
	}
	.chips {
		display: grid;
		grid-template-columns: repeat(5, 1fr);
		gap: 0.5rem;
	}
	.chip {
		padding: 0.625rem 0;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: #fff;
		font: inherit;
		font-weight: 600;
		cursor: pointer;
	}
	.chip:hover {
		background: var(--surface-2);
	}
	.chip.on {
		border-color: var(--teal);
		background: var(--mist);
		box-shadow: inset 0 0 0 1px var(--teal);
	}
	.money {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
	}
	.money input {
		flex: 1;
		min-width: 0;
		padding: 0.625rem 0.75rem;
		font: inherit;
	}
	.total {
		margin: 0;
		color: var(--text-soft);
		font-size: 0.9375rem;
	}
	.total strong {
		font-size: 1.375rem;
		color: var(--ink);
		font-variant-numeric: tabular-nums;
	}
	.seg {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
	}
	.seg button {
		display: grid;
		gap: 0.125rem;
		text-align: left;
		padding: 0.75rem 0.875rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: #fff;
		font: inherit;
		cursor: pointer;
	}
	.seg button span {
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.seg button:hover {
		background: var(--surface-2);
	}
	.seg button.on {
		border-color: var(--teal);
		background: var(--mist);
		box-shadow: inset 0 0 0 1px var(--teal);
	}
	.wide {
		width: 100%;
		padding-block: 0.75rem;
	}
	.pay {
		display: grid;
		gap: 1rem;
	}
	.summary {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.625rem 0.875rem;
		border-radius: 12px;
		background: var(--surface-2);
		font-weight: 600;
	}
	.link {
		font: inherit;
		font-size: 0.875rem;
		font-weight: 500;
		background: none;
		border: 0;
		padding: 0;
		color: var(--text-soft);
		text-decoration: underline;
		cursor: pointer;
	}
	.step {
		margin: 0;
		line-height: 1.45;
	}
	.card {
		display: grid;
		grid-template-columns: 8.5rem minmax(0, 1fr);
		gap: 1rem;
		align-items: center;
		padding: 0.875rem;
		border: 1px solid var(--rule);
		border-radius: 14px;
	}
	.card.stale {
		opacity: 0.55;
	}
	.qr {
		background: #fff;
		line-height: 0;
	}
	.qr :global(svg) {
		display: block;
		width: 100%;
		height: auto;
	}
	.where {
		display: grid;
		gap: 0.75rem;
		justify-items: start;
		min-width: 0;
	}
	.addr {
		margin: 0;
		font-family: ui-monospace, 'SF Mono', Menlo, monospace;
		font-size: 0.8125rem;
		line-height: 1.4;
		overflow-wrap: anywhere;
		user-select: all;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.note {
		margin: 0;
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--muted);
	}
	.wait {
		display: flex;
		gap: 0.625rem;
		align-items: flex-start;
		margin: 0;
		padding-top: 0.875rem;
		border-top: 1px solid var(--rule);
		font-size: 0.875rem;
		line-height: 1.4;
		color: var(--text-soft);
	}
	.pulse {
		flex: none;
		width: 0.625rem;
		height: 0.625rem;
		margin-top: 0.3rem;
		border-radius: 50%;
		background: var(--teal);
		animation: pulse 1.6s ease-in-out infinite;
	}
	@keyframes pulse {
		50% {
			opacity: 0.25;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.pulse {
			animation: none;
		}
	}
	.done {
		display: grid;
		justify-items: center;
		gap: 0.375rem;
		padding: 1rem 0 0.5rem;
		text-align: center;
	}
	.done .sub {
		margin: 0;
	}
	.check {
		display: grid;
		place-items: center;
		width: 3rem;
		height: 3rem;
		border-radius: 50%;
		background: var(--mist);
		color: var(--teal);
		font-size: 1.5rem;
		font-weight: 700;
	}
	.big {
		margin: 0.5rem 0 0;
		font-size: 1.75rem;
		font-weight: 700;
		letter-spacing: -0.02em;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
		margin-top: 1rem;
	}
	.history {
		border-top: 1px solid var(--rule);
		padding-top: 0.875rem;
		font-size: 0.875rem;
	}
	.history summary {
		cursor: pointer;
		font-weight: 600;
		color: var(--text-soft);
	}
	.history ul {
		list-style: none;
		margin: 0.5rem 0 0;
		padding: 0;
		font-variant-numeric: tabular-nums;
	}
	.history li {
		display: grid;
		grid-template-columns: 6.5rem 1fr auto 2.5rem;
		gap: 0.5rem;
		padding: 0.4375rem 0;
		border-bottom: 1px solid var(--rule);
	}
	.plus {
		color: var(--line-green);
		font-weight: 600;
	}
	@media (max-width: 460px) {
		.sheet {
			padding: 1.25rem 1rem;
		}
		.card {
			grid-template-columns: 1fr;
			justify-items: center;
		}
		.qr {
			width: 9rem;
		}
		.where {
			justify-items: stretch;
			width: 100%;
		}
		.history li {
			grid-template-columns: 1fr auto;
		}
	}
</style>
