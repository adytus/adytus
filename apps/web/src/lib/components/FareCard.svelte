<script lang="ts">
	import { usd, type Balance } from '../api.ts';

	// A slim fare card: what's left, and how much of what was bought is used.
	let { balance }: { balance: Balance | null } = $props();

	const used = $derived(
		balance && balance.purchased_micros > 0 ? Math.min(1, balance.used_micros / balance.purchased_micros) : 0
	);
</script>

<a class="fare ticket" href="/account" aria-label="Credit: {balance ? usd(balance.available_micros) : 'loading'} left. Open account">
	<div class="row">
		<span class="left">{balance ? usd(balance.available_micros) : '—'}</span>
		<span class="label">left</span>
		{#if balance && balance.held_micros > 0}
			<span class="held" title="Reserved for a reply in progress">{usd(balance.held_micros)} held</span>
		{/if}
	</div>
	{#if balance && balance.purchased_micros > 0}
		<div class="bar" aria-hidden="true"><span style:width="{used * 100}%"></span></div>
		<p class="sub">{usd(balance.used_micros)} used of {usd(balance.purchased_micros)}</p>
	{:else}
		<p class="sub">No credit yet. Add some to start chatting.</p>
	{/if}
</a>

<style>
	.fare {
		display: grid;
		gap: 0.375rem;
		padding: 0.625rem 0.75rem;
		text-decoration: none;
		border-left: 6px solid var(--line-yellow);
	}
	.fare:hover {
		background: #dcd8cf;
	}
	.row {
		display: flex;
		align-items: baseline;
		gap: 0.375rem;
	}
	.left {
		font-size: 1.375rem;
		font-weight: 900;
		font-stretch: 75%;
		letter-spacing: -0.02em;
		line-height: 1;
		font-variant-numeric: tabular-nums;
	}
	.label {
		font-size: 0.8125rem;
		font-weight: 600;
	}
	.held {
		margin-left: auto;
		font-size: 0.6875rem;
		font-weight: 700;
		background: var(--ink);
		color: var(--line-yellow);
		padding: 0.0625rem 0.375rem;
		border-radius: 3px;
	}
	.bar {
		height: 4px;
		background: rgb(0 0 0 / 0.15);
		border-radius: 2px;
		overflow: hidden;
	}
	.bar span {
		display: block;
		height: 100%;
		background: var(--ink);
	}
	.sub {
		margin: 0;
		font-size: 0.75rem;
		font-weight: 600;
		opacity: 0.75;
		font-variant-numeric: tabular-nums;
	}
</style>
