<script lang="ts">
	import { api, ApiError, usd, tokenCount, shortID, type LedgerEntry, type Plan } from '#lib/api.ts';
	import { app } from '#lib/state.svelte.ts';
	import Barcode from '#lib/components/Barcode.svelte';
	import RouteBadge from '#lib/components/RouteBadge.svelte';
	import RecoveryStub from '#lib/components/RecoveryStub.svelte';
	import Alert from '#lib/components/Alert.svelte';

	let plans = $state<Plan[]>([]);
	let entries = $state<LedgerEntry[]>([]);
	let buying = $state('');
	let bought = $state('');
	let error = $state('');

	let current = $state('');
	let next = $state('');
	let pwBusy = $state(false);
	let pwError = $state('');
	let newCode = $state('');

	$effect(() => {
		api.plans().then((r) => (plans = r.plans));
		loadLedger();
	});

	async function loadLedger() {
		entries = (await api.ledger()).entries;
	}

	async function subscribe(p: Plan) {
		error = '';
		bought = '';
		buying = p.id;
		try {
			const r = await api.subscribe(p.id);
			app.balance = r.balance;
			bought = `You subscribed for ${usd(p.price_micros, 0)}. ${tokenCount(p.credit_tokens)} tokens were added (receipt ${r.payment_ref}).`;
			await loadLedger();
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Payment failed. Try again.';
		} finally {
			buying = '';
		}
	}

	async function changePassword(e: SubmitEvent) {
		e.preventDefault();
		pwError = '';
		pwBusy = true;
		try {
			newCode = (await api.changePassword(current, next)).recovery_code;
			current = '';
			next = '';
		} catch (err) {
			pwError = err instanceof ApiError ? err.message : 'Could not change the password.';
		} finally {
			pwBusy = false;
		}
	}

	const lines = ['blue', 'purple', 'red', 'yellow'] as const;
	const b = $derived(app.balance);
	const usedPct = $derived(b && b.purchased_tokens > 0 ? Math.min(100, (b.used_tokens / b.purchased_tokens) * 100) : 0);
	const fmtDate = (s: string) =>
		new Date(s).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
</script>

<div class="page">
	<header>
		<h1>Account</h1>
		<p class="muted">Signed in as {app.user?.email}</p>
	</header>

	<section aria-labelledby="usage-h">
		<h2 id="usage-h">Usage</h2>
		{#if b && b.purchased_tokens > 0}
			<p class="used"><strong>{tokenCount(b.used_tokens)}</strong> used of {tokenCount(b.purchased_tokens)} tokens</p>
			<div class="meter" role="img" aria-label="{Math.round(usedPct)}% of your tokens used">
				<span style:width="{usedPct}%"></span>
			</div>
			<dl class="facts">
				<div><dt>Left to spend</dt><dd>{tokenCount(b.available_tokens)} tokens</dd></div>
				{#if b.held_tokens > 0}<div><dt>Held for a reply</dt><dd>{tokenCount(b.held_tokens)} tokens</dd></div>{/if}
				<div><dt>Spent today</dt><dd>{tokenCount(b.spent_today_tokens)} of {tokenCount(b.daily_cap_tokens)} tokens</dd></div>
			</dl>
		{:else}
			<p class="muted">You don't have any tokens yet. Subscribe below to start chatting.</p>
		{/if}
	</section>

	<section aria-labelledby="plans-h">
		<h2 id="plans-h">Add tokens</h2>
		<p class="muted">
			Tokens are spent as models reply and don't expire. Payment is simulated in this build, so
			no card is needed.
		</p>
		<div class="plans">
			{#each plans as p, i (p.id)}
				<article class="ticket plan">
					<div class="top">
						<RouteBadge line={lines[i % lines.length]} size={2.25}>{p.name[0]}</RouteBadge>
						<span class="micro">Single fare · no expiry</span>
					</div>
					<p class="credit">{tokenCount(p.credit_tokens)}</p>
					<p class="of">tokens for {usd(p.price_micros, 0)}</p>
					<div class="foot">
						<Barcode value={p.id.toUpperCase()} height={18} />
						<button class="btn primary" disabled={buying !== ''} onclick={() => subscribe(p)}>
							{buying === p.id ? 'Paying…' : `Subscribe for ${usd(p.price_micros, 0)}`}
						</button>
					</div>
				</article>
			{/each}
		</div>
		{#if bought}<Alert tone="ok" title="Payment complete" ondismiss={() => (bought = '')}><p>{bought}</p></Alert>{/if}
		{#if error}<Alert title="Payment failed" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}
		<p class="muted">The daily spending limit resets at midnight UTC.</p>
	</section>

	<section aria-labelledby="ledger-h">
		<h2 id="ledger-h">Usage and payments</h2>
		{#if entries.length === 0}
			<p class="muted">Nothing yet. Each reply you receive is listed here with its token counts.</p>
		{:else}
			<div class="table-wrap">
				<table>
					<thead>
						<tr>
							<th scope="col">When</th>
							<th scope="col">Item</th>
							<th scope="col" class="num">Tokens in</th>
							<th scope="col" class="num">Tokens out</th>
							<th scope="col" class="num">Tokens</th>
						</tr>
					</thead>
					<tbody>
						{#each entries as e (e.id)}
							<tr>
								<td>{fmtDate(e.created_at)}</td>
								<td>
									{#if e.reason === 'usage'}{app.model(e.model ?? '')?.name ?? e.model}
									{:else if e.reason === 'subscription'}Credit purchase
									{:else}Adjustment{/if}
								</td>
								<td class="num">{e.prompt_tokens?.toLocaleString() ?? ''}</td>
								<td class="num">{e.completion_tokens?.toLocaleString() ?? ''}</td>
								<td class="num" class:plus={e.delta_tokens > 0}>
									{e.delta_tokens > 0 ? '+' : '−'}{tokenCount(Math.abs(e.delta_tokens))}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>

	<section aria-labelledby="pw-h">
		<h2 id="pw-h">Change password</h2>
		{#if newCode}
			<RecoveryStub code={newCode} onclose={() => (newCode = '')} />
		{:else}
			<form class="pw" onsubmit={changePassword}>
				<p class="muted">
					Your data is re-keyed with the new password. You'll get a new recovery code, and other
					devices are signed out.
				</p>
				<label class="field">
					Current password
					<input type="password" autocomplete="current-password" required bind:value={current} />
				</label>
				<label class="field">
					New password
					<input type="password" autocomplete="new-password" minlength="12" required bind:value={next} />
				</label>
				{#if pwError}<Alert title="Password not changed"><p>{pwError}</p></Alert>{/if}
				<button class="btn primary" disabled={pwBusy}>{pwBusy ? 'Re-keying…' : 'Change password'}</button>
			</form>
		{/if}
	</section>

	<p class="micro id">Account {shortID(app.user?.id ?? '')}</p>
</div>

<style>
	.page {
		max-width: 56rem;
		margin: 0 auto;
		padding: 2rem 1.5rem 4rem;
		display: grid;
		gap: 2.5rem;
	}
	h1 {
		margin: 0;
		font-size: clamp(2.5rem, 6vw, 4rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.03em;
		color: var(--headline);
	}
	h2 {
		margin: 0 0 0.5rem;
		font-size: 1.375rem;
		font-weight: 800;
		font-stretch: 85%;
	}
	.muted {
		color: var(--muted);
		margin: 0.25rem 0 1rem;
		line-height: 1.5;
		max-width: 62ch;
	}
	.plans {
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	.plan {
		width: min(100%, 20rem);
		padding: 1rem 1.25rem 0;
	}
	.top {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}
	.credit {
		margin: 0.75rem 0 0;
		font-size: 3.25rem;
		font-weight: 900;
		font-stretch: 65%;
		letter-spacing: -0.03em;
		line-height: 0.9;
	}
	.of {
		margin: 0;
		font-weight: 600;
		font-size: 0.875rem;
	}
	.used {
		margin: 0 0 0.5rem;
		font-size: 1.125rem;
		color: var(--text-soft);
	}
	.used strong {
		font-size: 2.25rem;
		font-weight: 900;
		font-stretch: 75%;
		color: var(--paper);
		font-variant-numeric: tabular-nums;
	}
	.meter {
		height: 8px;
		border-radius: 4px;
		background: var(--surface-2);
		overflow: hidden;
		max-width: 32rem;
	}
	.meter span {
		display: block;
		height: 100%;
		background: var(--line-yellow);
	}
	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem 2rem;
		margin: 1rem 0 0;
	}
	.facts dt {
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.facts dd {
		margin: 0.125rem 0 0;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}
	.foot {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		margin-top: 1rem;
		padding: 0.75rem 0;
		border-top: 2px dashed rgb(0 0 0 / 0.35);
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
	}
	td {
		padding: 0.5rem 0.75rem 0.5rem 0;
		border-bottom: 1px solid var(--rule);
		white-space: nowrap;
	}
	.num {
		text-align: right;
	}
	.plus {
		color: var(--line-green);
	}
	.pw {
		display: grid;
		gap: 0.875rem;
		max-width: 24rem;
	}
	.pw .muted {
		margin: 0;
	}
	.pw .btn {
		justify-self: start;
	}
	.id {
		color: var(--muted);
	}
	@media (max-width: 820px) {
		.page {
			padding: 1.5rem 1rem 3rem;
		}
	}
</style>
