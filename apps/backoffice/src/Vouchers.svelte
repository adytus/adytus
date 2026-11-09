<script lang="ts">
	import Alert from '../../web/src/lib/components/Alert.svelte';
	import { api, type VoucherList } from './api.ts';

	let data = $state<VoucherList | null>(null);
	let busy = $state(false);
	let error = $state('');
	let code = $state('');
	let percent = $state(50);
	let expires = $state('');

	async function run(fn: () => Promise<VoucherList>) {
		busy = true;
		error = '';
		try {
			data = await fn();
			return true;
		} catch (err) {
			error = (err as Error).message;
			return false;
		} finally {
			busy = false;
		}
	}

	$effect(() => {
		void run(api.vouchers);
	});

	async function create(e: SubmitEvent) {
		e.preventDefault();
		const done = await run(() => api.createVoucher(code, percent, new Date(expires).toISOString()));
		if (done) code = '';
	}

	function end(id: string, name: string) {
		if (!confirm(`End ${name} now? Payments already on their way keep the discount for 30 minutes.`)) return;
		void run(() => api.endVoucher(id));
	}

	const usd = (micros: number) =>
		`$${(micros / 1_000_000).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
	const date = (s: string) =>
		new Date(s).toLocaleString(undefined, { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' });
	const running = (s: string) => new Date(s).getTime() > Date.now();
</script>

<section aria-labelledby="vouchers-h">
	<h2 id="vouchers-h">Vouchers</h2>
	<p class="muted">
		Public codes for the Add tokens dialog. Any account can use a running code on every top-up until
		it expires. At 50% off a user pays $10 and gets $20 of tokens, so check that model prices are
		high enough to cover the discount.
	</p>

	{#if error}<Alert title="Not saved" ondismiss={() => (error = '')}><p>{error}</p></Alert>{/if}

	{#if data && !data.enabled}
		<Alert tone="info" title="Crypto payments are off">
			<p>Vouchers apply to crypto top-ups. Set BASE_RPC_URL and BASE_XPUB on the server to turn them on.</p>
		</Alert>
	{:else if data}
		<form onsubmit={create}>
			<label>
				Code
				<input required minlength="3" maxlength="32" pattern="[A-Za-z0-9_\-]+" spellcheck="false" placeholder="SOCIAL50" bind:value={code} />
			</label>
			<label>
				Percent off
				<input required type="number" min="1" max="90" step="1" bind:value={percent} />
			</label>
			<label>
				Expires
				<input required type="datetime-local" bind:value={expires} />
			</label>
			<button class="btn primary" disabled={busy}>Create</button>
		</form>

		{#if data.vouchers.length === 0}
			<p class="muted">No vouchers yet.</p>
		{:else}
			<div class="table-wrap">
				<table>
					<thead>
						<tr>
							<th scope="col">Code</th>
							<th scope="col" class="num">Off</th>
							<th scope="col">Expires</th>
							<th scope="col" class="num">Top-ups</th>
							<th scope="col" class="num">Paid</th>
							<th scope="col" class="num">Bonus given</th>
							<th scope="col"><span class="sr-only">Actions</span></th>
						</tr>
					</thead>
					<tbody>
						{#each data.vouchers as v (v.id)}
							<tr class:off={!running(v.expires_at)}>
								<td><code>{v.code}</code></td>
								<td class="num">{v.percent}%</td>
								<td>{running(v.expires_at) ? date(v.expires_at) : `Ended ${date(v.expires_at)}`}</td>
								<td class="num">{v.uses}</td>
								<td class="num">{usd(v.paid_micros)}</td>
								<td class="num">{usd(v.bonus_micros)}</td>
								<td>
									{#if running(v.expires_at)}
										<button class="btn danger" disabled={busy} onclick={() => end(v.id, v.code)}>End now</button>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	{:else if !error}
		<p class="muted">Loading…</p>
	{/if}
</section>

<style>
	section {
		display: grid;
		gap: 1rem;
	}
	h2 {
		margin: 0;
		font-size: 1.375rem;
		font-weight: 800;
		font-stretch: 85%;
	}
	.muted {
		color: var(--muted);
		margin: 0;
		line-height: 1.5;
		max-width: 65ch;
	}
	form {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: 0.75rem;
	}
	label {
		display: grid;
		gap: 0.25rem;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--muted);
	}
	input {
		padding: 0.5rem 0.625rem;
		font: inherit;
		font-size: 0.875rem;
	}
	input[type='number'] {
		width: 6rem;
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
		white-space: nowrap;
	}
	.num {
		text-align: right;
	}
	.off {
		color: var(--muted);
	}
	code {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-weight: 700;
	}
</style>
