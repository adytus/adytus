<script lang="ts">
	import Alert from '../../web/src/lib/components/Alert.svelte';
	import { api, nearRPCs, nearUSDC, type NearCheck, type NearSettings, type NearState } from './api.ts';

	// NEAR payments: every setting lives in the database and is edited here.
	// Money is shown in dollars and stored in micro-USD.

	let data = $state<NearState | null>(null);
	let form = $state<NearSettings | null>(null);
	let loading = $state(false);
	let saving = $state(false);
	let testing = $state(false);
	let error = $state('');
	let saved = $state(false);
	let checks = $state<NearCheck[] | null>(null);
	let copied = $state('');

	// Dollar and minute views of the stored units, bound to the inputs.
	let minUSD = $state(0);
	let dustUSD = $state(0);
	let ttlMin = $state(0);
	let tolPct = $state(0);

	function adopt(s: NearState) {
		data = s;
		form = { ...s.settings };
		minUSD = s.settings.min_micros / 1e6;
		dustUSD = s.settings.dust_micros / 1e6;
		ttlMin = s.settings.quote_ttl_secs / 60;
		tolPct = s.settings.tolerance_bps / 100;
	}

	async function load() {
		loading = true;
		error = '';
		try {
			adopt(await api.near());
		} catch (err) {
			error = (err as Error).message;
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		void load();
	});

	// The form as the API wants it.
	function payload(): NearSettings {
		const f = form!;
		return {
			...f,
			min_micros: Math.round(minUSD * 1e6),
			dust_micros: Math.round(dustUSD * 1e6),
			quote_ttl_secs: Math.round(ttlMin * 60),
			tolerance_bps: Math.round(tolPct * 100),
			max_price_age_secs: Math.round(f.max_price_age_secs),
			poll_secs: Math.round(f.poll_secs)
		};
	}

	const dirty = $derived.by(() => {
		if (!form || !data) return false;
		const p = payload();
		const s = data.settings;
		return (Object.keys(p) as (keyof NearSettings)[]).some((k) => k !== 'updated_at' && p[k] !== s[k]);
	});

	async function save(e?: SubmitEvent) {
		e?.preventDefault();
		if (!form) return;
		saving = true;
		error = '';
		saved = false;
		try {
			adopt(await api.putNear(payload()));
			saved = true;
			setTimeout(() => (saved = false), 2500);
		} catch (err) {
			error = (err as Error).message;
		} finally {
			saving = false;
		}
	}

	async function test() {
		if (!form) return;
		testing = true;
		error = '';
		checks = null;
		try {
			checks = (await api.testNear(payload())).checks;
		} catch (err) {
			error = (err as Error).message;
		} finally {
			testing = false;
		}
	}

	async function toggle() {
		if (!form || !data) return;
		const on = !data.settings.enabled;
		if (on && dirty) {
			error = 'Save or undo your changes first.';
			return;
		}
		if (!on && !confirm('Switch NEAR payments off? Users can no longer pick NEAR. Payments already sent are still credited once it is back on.')) return;
		form.enabled = on;
		await save();
		if (error) form.enabled = !on;
	}

	function pickNetwork(n: NearSettings['network']) {
		if (!form || form.network === n) return;
		const wasKnownRPC = nearRPCs[form.network].some((e) => e.url === form!.rpc_url);
		const wasKnownUSDC = form.usdc === nearUSDC[form.network];
		form.network = n;
		if (wasKnownRPC) form.rpc_url = nearRPCs[n][0].url;
		if (wasKnownUSDC) form.usdc = nearUSDC[n];
	}

	const usd = (micros: number) =>
		`$${(micros / 1_000_000).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
	const YOCTO = 10n ** 24n;
	const nearText = (yocto: string, places = 4) => {
		const y = BigInt(yocto);
		const frac = (y % YOCTO).toString().padStart(24, '0').slice(0, places);
		return `${y / YOCTO}.${frac}`;
	};
	const nearUSD = (yocto: string, rate: number) => Number((BigInt(yocto) * BigInt(rate)) / YOCTO);
	// Storage costs 1 NEAR per 100 kB and can't be withdrawn.
	const lockedYocto = (bytes: number) => (BigInt(bytes) * YOCTO) / 100_000n;
	const amountText = (p: { asset: string; amount: string }) =>
		p.asset === 'near' ? `${nearText(p.amount)} NEAR` : p.asset === 'usdc' ? `${(Number(p.amount) / 1e6).toLocaleString()} USDC` : `${p.amount} units`;
	const date = (s: string) => new Date(s).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
	const explorer = $derived(data?.settings.network === 'testnet' ? 'https://testnet.nearblocks.io' : 'https://nearblocks.io');
	const statusText: Record<string, string> = {
		credited: 'Credited',
		ignored: 'Too small',
		pending: 'Not read yet',
		unmatched: 'No user'
	};

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

<section aria-labelledby="near-h">
	<div class="row">
		<h2 id="near-h">NEAR</h2>
		<button class="btn" disabled={loading} onclick={load}>{loading ? 'Reading the chain…' : 'Refresh'}</button>
	</div>
	<p class="muted">
		NEAR and USDC on NEAR, paid to one deposit contract with each user's memo. Everything here is
		stored in the database; nothing comes from the server environment. Deploy
		<code>contracts/near-deposit</code> first, as its README describes.
	</p>

	{#if error}<Alert title="NEAR settings" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}

	{#if loading && !data}
		<p class="muted">Loading…</p>
	{:else if data && form}
		{@const o = data.overview}
		<div class="status" class:on={data.settings.enabled}>
			<div>
				<strong>{data.settings.enabled ? 'On' : 'Off'}</strong>
				<span>
					{data.settings.enabled
						? `Users can pay with NEAR${data.settings.usdc ? ' and USDC' : ''} on ${data.settings.network}.`
						: data.settings.contract
							? 'Users don’t see NEAR in Add tokens.'
							: 'Deploy contracts/near-deposit, then save its account below to switch this on.'}
				</span>
			</div>
			<button
				class="btn"
				class:primary={!data.settings.enabled}
				class:danger={data.settings.enabled}
				type="button"
				disabled={saving || (!data.settings.enabled && !data.settings.contract)}
				title={!data.settings.enabled && !data.settings.contract ? 'Save a deposit contract first' : undefined}
				onclick={toggle}
			>
				{data.settings.enabled ? 'Switch off' : 'Switch on'}
			</button>
		</div>

		{#if o.contract}
			<dl class="stats">
				<div>
					<dt>NEAR on the contract</dt>
					<dd>
						{nearText(o.near_yocto, 2)}
						{#if o.near_usd_micros > 0}<span class="sub">≈ {usd(nearUSD(o.near_yocto, o.near_usd_micros))}</span>{/if}
					</dd>
					{#if o.storage_bytes}<span class="note">{nearText(lockedYocto(o.storage_bytes).toString(), 3)} held for storage</span>{/if}
				</div>
				{#if o.usdc}<div><dt>USDC on the contract</dt><dd>{usd(o.usdc_micros)}</dd></div>{/if}
				<div><dt>Credited to users</dt><dd>{usd(o.credited_micros)}</dd><span class="note">{o.credited} payment{o.credited === 1 ? '' : 's'}</span></div>
				<div>
					<dt>Payments read</dt>
					<dd>{o.read} <span class="sub">of {o.logged}</span></dd>
					{#if o.near_usd_micros > 0}<span class="note">NEAR is {usd(o.near_usd_micros)}</span>{/if}
				</div>
			</dl>
			{#if o.balance_error}<p class="err">Could not read the contract: {o.balance_error}</p>{/if}
		{/if}

		<form class="settings" onsubmit={save}>
			<fieldset>
				<legend>Chain</legend>
				<div class="grid">
					<label class="field">
						Network
						<select value={form.network} onchange={(e) => pickNetwork(e.currentTarget.value as NearSettings['network'])}>
							<option value="mainnet">Mainnet</option>
							<option value="testnet">Testnet</option>
						</select>
					</label>
					<label class="field">
						RPC URL
						<input type="url" required bind:value={form.rpc_url} list="near-rpcs" spellcheck="false" autocomplete="off" />
						<datalist id="near-rpcs">
							{#each nearRPCs[form.network] as e (e.url)}<option value={e.url}>{e.label}</option>{/each}
						</datalist>
					</label>
					<label class="field wide">
						Deposit contract
						<input bind:value={form.contract} placeholder="pay.example.near" spellcheck="false" autocomplete="off" />
						<span class="hint">The account contracts/near-deposit is deployed to. Its key moves funds out; keep it offline.</span>
					</label>
					<label class="field wide">
						USDC token
						<input bind:value={form.usdc} placeholder="Empty takes NEAR only" spellcheck="false" autocomplete="off" />
						<span class="hint">
							A 6-decimal, $1 token the contract accepts.
							{#if form.usdc !== nearUSDC[form.network]}
								<button class="link" type="button" onclick={() => (form!.usdc = nearUSDC[form!.network])}>Use Circle’s {form.network} USDC</button>
							{/if}
						</span>
					</label>
				</div>
			</fieldset>

			<fieldset>
				<legend>Price</legend>
				<div class="grid">
					<label class="field">
						Price source
						<select
							value={form.price_url === 'exchanges' ? 'exchanges' : 'hermes'}
							onchange={(e) => {
								if (e.currentTarget.value === 'exchanges') form!.price_url = 'exchanges';
								else if (form!.price_url === 'exchanges') form!.price_url = 'https://hermes.pyth.network';
							}}
						>
							<option value="exchanges">Exchanges (Coinbase, Kraken, CoinGecko)</option>
							<option value="hermes">Pyth Hermes server</option>
						</select>
						<span class="hint">
							{form.price_url === 'exchanges'
								? 'The middle of the three live prices. No quote is given if they differ by more than 3%.'
								: 'Pyth’s public Hermes now asks for a key; use a Hermes you run or have access to.'}
						</span>
					</label>
					{#if form.price_url !== 'exchanges'}
						<label class="field">
							Hermes URL
							<input type="url" required bind:value={form.price_url} spellcheck="false" autocomplete="off" />
						</label>
						<label class="field">
							Oldest price accepted (seconds)
							<input type="number" required min="10" max="86400" step="1" bind:value={form.max_price_age_secs} />
						</label>
						<label class="field wide">
							NEAR/USD feed ID
							<input required bind:value={form.price_feed} spellcheck="false" autocomplete="off" pattern="(0x)?[0-9a-fA-F]{'{64}'}" />
						</label>
					{/if}
				</div>
			</fieldset>

			<fieldset>
				<legend>Top-ups</legend>
				<div class="grid">
					<label class="field">
						Smallest top-up ($)
						<input type="number" required min="0.01" max="10000" step="0.01" bind:value={minUSD} />
					</label>
					<label class="field">
						Ignore payments under ($)
						<input type="number" required min="0" step="0.01" bind:value={dustUSD} />
						<span class="hint">Logged, not credited.</span>
					</label>
					<label class="field">
						Hold a NEAR price for (minutes)
						<input type="number" required min="1" max="1440" step="1" bind:value={ttlMin} />
					</label>
					<label class="field">
						Short payment still covered (%)
						<input type="number" required min="0" max="10" step="0.01" bind:value={tolPct} />
						<span class="hint">How far under a quote a NEAR payment can fall and still get the full amount.</span>
					</label>
					<label class="field">
						Check for payments every (seconds)
						<input type="number" required min="1" max="600" step="1" bind:value={form.poll_secs} />
					</label>
				</div>
			</fieldset>

			{#if checks}
				<ul class="checks" role="status">
					{#each checks as c (c.name)}
						<li class:ok={c.ok}><span aria-hidden="true">{c.ok ? '✓' : '✕'}</span><strong>{c.name}</strong> {c.detail}</li>
					{/each}
				</ul>
			{/if}

			<div class="actions">
				<button class="btn primary" type="submit" disabled={saving || !dirty}>{saving ? 'Saving…' : saved ? 'Saved' : 'Save'}</button>
				<button class="btn" type="button" disabled={testing} onclick={test}>{testing ? 'Testing…' : 'Test these settings'}</button>
				{#if dirty}<button class="btn" type="button" onclick={() => adopt(data!)}>Undo changes</button>{/if}
			</div>
		</form>

		{#if o.recent.length > 0}
			<h3>Latest payments</h3>
			<div class="table-wrap">
				<table>
					<thead>
						<tr>
							<th scope="col">#</th>
							<th scope="col">When</th>
							<th scope="col">User</th>
							<th scope="col" class="num">Paid</th>
							<th scope="col" class="num">Credited</th>
							<th scope="col">From</th>
						</tr>
					</thead>
					<tbody>
						{#each o.recent as p (p.index)}
							<tr>
								<td>{p.index}</td>
								<td>{date(p.at)}<span class="sub">block {p.block_height}</span></td>
								<td>
									{#if p.email}<span class="email">{p.email}</span>{:else}<span class="err">No user</span>{/if}
									<span class="sub">memo {p.memo}</span>
								</td>
								<td class="num">{amountText(p)}{#if p.asset === 'other'}<span class="sub" title={p.token}>unpriced token</span>{/if}</td>
								<td class="num" class:err={p.status === 'unmatched'}>
									{p.status === 'credited' ? usd(p.credit_micros) : statusText[p.status]}
								</td>
								<td class="addr">
									<a href="{explorer}/address/{p.sender}" target="_blank" rel="noopener noreferrer" title={p.sender}>{p.sender.length > 24 ? `${p.sender.slice(0, 10)}…${p.sender.slice(-8)}` : p.sender}</a>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			<p class="muted">
				"No user" means nobody owns that memo, or the token isn't the USDC set above. Such
				payments sit on the contract uncredited; refund or credit them by hand.
			</p>
		{/if}

		{#if o.contract}
			<p class="muted">
				Contract <code>{o.contract}</code>
				<button class="link" type="button" onclick={() => copy(o.contract)}>{copied === o.contract ? 'Copied' : 'Copy'}</button>
				· <a href="{explorer}/address/{o.contract}" target="_blank" rel="noopener noreferrer">NearBlocks</a>.
				Funds leave only with the contract account's own key; see the contract's README.
			</p>
		{/if}
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
		margin: 0.5rem 0 0;
		font-size: 1.0625rem;
		font-weight: 700;
	}
	.muted {
		color: var(--muted);
		margin: 0;
		line-height: 1.5;
		max-width: 65ch;
	}
	code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.8125rem;
	}
	.status {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 1rem;
		padding: 0.875rem 1rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: var(--surface);
	}
	.status > div {
		display: grid;
		gap: 0.125rem;
	}
	.status strong {
		font-size: 1.0625rem;
	}
	.status.on strong {
		color: var(--line-green);
	}
	.status span {
		color: var(--muted);
		font-size: 0.875rem;
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
	.note {
		display: block;
		margin-top: 0.125rem;
		font-size: 0.75rem;
		color: var(--muted);
	}
	.settings {
		display: grid;
		gap: 1.25rem;
		padding: 1rem 1.25rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: var(--surface);
	}
	fieldset {
		border: 0;
		margin: 0;
		padding: 0;
		min-width: 0;
	}
	legend {
		padding: 0;
		margin-bottom: 0.625rem;
		font-size: 0.8125rem;
		font-weight: 700;
		color: var(--muted);
		letter-spacing: 0.04em;
		text-transform: uppercase;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr));
		gap: 0.875rem 1rem;
	}
	.grid .wide {
		grid-column: 1 / -1;
	}
	.field input,
	.field select {
		width: 100%;
	}
	.hint {
		font-size: 0.75rem;
		font-weight: 400;
		color: var(--muted);
		line-height: 1.4;
	}
	.checks {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.375rem;
		font-size: 0.875rem;
		line-height: 1.4;
	}
	.checks li {
		display: flex;
		gap: 0.5rem;
		color: var(--line-red);
		overflow-wrap: anywhere;
	}
	.checks li.ok {
		color: var(--line-green);
	}
	.checks strong {
		flex: none;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
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
		margin: 0;
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
	.addr a {
		color: inherit;
	}
	.link {
		font: inherit;
		font-size: inherit;
		background: none;
		border: 0;
		padding: 0;
		color: inherit;
		text-decoration: underline;
		cursor: pointer;
	}
</style>
