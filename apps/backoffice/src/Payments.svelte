<script lang="ts">
	import Alert from '../../web/src/lib/components/Alert.svelte';
	import { api, type CryptoOverview } from './api.ts';

	let data = $state<CryptoOverview | null>(null);
	let busy = $state(false);
	let error = $state('');
	let copied = $state('');

	async function load() {
		busy = true;
		error = '';
		try {
			data = await api.crypto();
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = false;
		}
	}

	$effect(() => {
		void load();
	});

	const usd = (micros: number) =>
		`$${(micros / 1_000_000).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
	const eth = (wei: string) => {
		const w = BigInt(wei);
		const whole = w / 10n ** 18n;
		const frac = (w % 10n ** 18n).toString().padStart(18, '0').slice(0, 6);
		return `${whole}.${frac}`;
	};
	const date = (s: string | null) =>
		s ? new Date(s).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '—';
	const short = (a: string) => `${a.slice(0, 6)}…${a.slice(-4)}`;
	const explorer = $derived(data?.chain_id === 84532 ? 'https://sepolia.basescan.org' : 'https://basescan.org');

	// An address holding USDC but almost no ETH cannot pay the fee to send it.
	const GAS_WEI = 30_000_000_000_000n; // 0.00003 ETH
	const needsGas = (a: { usdc_micros: number; eth_wei: string }) => a.usdc_micros > 0 && BigInt(a.eth_wei) < GAS_WEI;
	const holds = (a: { usdc_micros: number; eth_wei: string }) => a.usdc_micros > 0 || BigInt(a.eth_wei) > 0n;

	async function copy(text: string) {
		try {
			await navigator.clipboard.writeText(text);
			copied = text;
			setTimeout(() => (copied = ''), 1500);
		} catch {
			// Clipboard blocked; the text is selectable.
		}
	}
</script>

<section aria-labelledby="pay-h">
	<div class="row">
		<h2 id="pay-h">Payments</h2>
		<button class="btn" disabled={busy} onclick={load}>{busy ? 'Reading the chain…' : 'Refresh'}</button>
	</div>
	<p class="muted">
		USDC and ETH on Base received at each user's deposit address. Balances are read from the chain
		when you open or refresh this tab. Tokens are credited to users separately, after
		confirmation.
	</p>

	{#if error}<Alert title="Could not load payments" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}

	{#if busy && !data}
		<p class="muted">Loading…</p>
	{:else if data && !data.enabled}
		<Alert tone="info" title="Crypto payments are off">
			<p>Set BASE_RPC_URL and BASE_XPUB on the server to turn them on.</p>
		</Alert>
	{:else if data}
		<dl class="stats">
			<div><dt>On-chain now</dt><dd>{usd(data.totals.on_chain_micros)}</dd></div>
			<div><dt>USDC</dt><dd>{usd(data.totals.usdc_micros)}</dd></div>
			<div>
				<dt>ETH</dt>
				<dd>{eth(data.totals.eth_wei)} <span class="sub">≈ {usd(data.totals.eth_usd_micros)}</span></dd>
			</div>
			<div><dt>Credited to users</dt><dd>{usd(data.totals.credited_micros)}</dd></div>
		</dl>

		{#if data.addresses.length === 0}
			<p class="muted">No deposit addresses yet. One is created when a user first opens Add tokens.</p>
		{:else}
			<div class="table-wrap">
				<table>
					<thead>
						<tr>
							<th scope="col">User</th>
							<th scope="col">Wallet account</th>
							<th scope="col" class="num">Credited</th>
							<th scope="col" class="num">USDC</th>
							<th scope="col" class="num">ETH</th>
							<th scope="col">Address</th>
						</tr>
					</thead>
					<tbody>
						{#each data.addresses as a (a.user_id)}
							<tr>
								<td>
									<span class="email">{a.email}</span>
									<span class="sub">{a.deposits} {a.deposits === 1 ? 'payment' : 'payments'} · last {date(a.last_deposited_at)}</span>
								</td>
								<td>
									Account {a.account_number}
									<span class="sub">index {a.index}</span>
								</td>
								<td class="num">
									{usd(a.credited_micros)}
									<span class="sub">{a.credited_tokens.toLocaleString()} tokens</span>
								</td>
								{#if a.balance_error}
									<td class="num err" colspan="2" title={a.balance_error}>Could not read</td>
								{:else}
									<td class="num">
										{usd(a.usdc_micros)}
										{#if needsGas(a)}<span class="tag">needs ETH for gas</span>{/if}
									</td>
									<td class="num">
										{eth(a.eth_wei)}
										{#if a.eth_usd_micros > 0}<span class="sub">{usd(a.eth_usd_micros)}</span>{/if}
									</td>
								{/if}
								<td class="addr">
									<code title={a.address}>{short(a.address)}</code>
									<button class="link" type="button" onclick={() => copy(a.address)}>{copied === a.address ? 'Copied' : 'Copy'}</button>
									<a href="{explorer}/address/{a.address}" target="_blank" rel="noopener noreferrer">Basescan</a>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}

		<div class="how">
			<h3>How to move the money</h3>
			<p class="muted">
				These addresses all come from one seed phrase. They are not held by this server, so moving
				funds happens in a wallet, not here.
			</p>
			<ol>
				<li>
					Import the seed phrase into MetaMask, Rabby or a Ledger. The Wallet account column says
					which account to open: Account 1 is index 0, which no user uses, so user addresses start
					at <strong>Account 2</strong>. Use Add account until you reach the one you need.
				</li>
				<li>
					Switch the wallet to the <strong>Base</strong> network (chain {data.chain_id}). To see USDC, import the token
					<code>{data.usdc}</code>.
				</li>
				<li>
					Check the address in the wallet matches the one in the table. If it does not, stop: the
					seed phrase is not the one BASE_XPUB was made from.
				</li>
				<li>
					Send the ETH or USDC to wherever you hold your funds. Sending USDC costs a little ETH
					for the network fee, a fraction of a cent. Rows tagged "needs ETH for gas" have USDC but
					too little ETH, so send a tiny amount of ETH to that address first.
				</li>
				<li>
					Avoid moving ETH out of an address while its user has an open ETH price quote (15
					minutes), or the deposit can be missed. Moving funds out never changes what users were
					already credited.
				</li>
			</ol>
			<p class="muted">
				Derivation path: <code>{data.path}</code>. Rows with funds on-chain but nothing credited mean a payment
				the server has not seen or confirmed yet.
			</p>
		</div>
		{#if data.addresses.some(holds)}<p class="muted">{data.addresses.filter(holds).length} of {data.addresses.length} addresses hold funds.</p>{/if}
	{/if}
</section>

<style>
	section {
		display: grid;
		gap: 1rem;
	}
	.row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
	}
	h2 {
		margin: 0;
		font-size: 1.375rem;
		font-weight: 800;
		font-stretch: 85%;
	}
	h3 {
		margin: 0;
		font-size: 1.0625rem;
		font-weight: 700;
	}
	.muted {
		color: var(--muted);
		margin: 0;
		line-height: 1.5;
		max-width: 65ch;
	}
	.stats {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.75rem;
		margin: 0;
	}
	.stats > div {
		padding: 0.875rem 1rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: var(--surface);
	}
	.stats dt {
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.stats dd {
		margin: 0.25rem 0 0;
		font-size: 1.375rem;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}
	.stats dd .sub {
		display: inline;
		font-size: 0.8125rem;
		font-weight: 500;
	}
	.table-wrap {
		overflow-x: auto;
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
		font-variant-numeric: tabular-nums;
	}
	th {
		text-align: left;
		font-weight: 600;
		color: var(--muted);
		padding: 0.5rem 0.75rem 0.5rem 0;
		border-bottom: 2px solid var(--rule);
		white-space: nowrap;
	}
	td {
		padding: 0.6rem 0.75rem 0.6rem 0;
		border-bottom: 1px solid var(--rule);
		vertical-align: top;
		white-space: nowrap;
	}
	.num {
		text-align: right;
	}
	.err {
		color: var(--line-red);
	}
	.email {
		display: block;
		font-weight: 700;
	}
	.sub {
		display: block;
		font-size: 0.75rem;
		color: var(--muted);
	}
	.tag {
		display: block;
		margin-top: 0.125rem;
		font-size: 0.6875rem;
		font-weight: 700;
		color: var(--line-red);
	}
	.addr {
		display: flex;
		gap: 0.625rem;
		align-items: baseline;
	}
	code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.8125rem;
	}
	.link {
		font: inherit;
		font-size: 0.8125rem;
		background: none;
		border: 0;
		padding: 0;
		color: inherit;
		text-decoration: underline;
		cursor: pointer;
	}
	.how {
		display: grid;
		gap: 0.625rem;
		padding: 1rem 1.25rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: var(--surface);
	}
	.how ol {
		margin: 0;
		padding-left: 1.25rem;
		display: grid;
		gap: 0.5rem;
		line-height: 1.5;
		max-width: 70ch;
	}
</style>
