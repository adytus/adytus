<script lang="ts">
	import { usd, shortID, type Balance } from '../api.ts';
	import Barcode from './Barcode.svelte';
	import RouteBadge from './RouteBadge.svelte';

	let { balance, userID }: { balance: Balance | null; userID: string } = $props();

	const capUsed = $derived(
		balance && balance.daily_cap_micros > 0
			? Math.min(1, balance.spent_today_micros / balance.daily_cap_micros)
			: 0
	);
</script>

<a class="fare ticket" href="/account" aria-label="Balance and plans">
	<div class="head">
		<span class="micro">Fare card</span>
		<RouteBadge line="yellow" size={1.25}>$</RouteBadge>
	</div>
	<p class="amount">{balance ? usd(balance.available_micros) : '—'}</p>
	<p class="micro sub">
		{#if balance && balance.held_micros > 0}
			{usd(balance.held_micros)} held for a reply in progress
		{:else}
			Available credit
		{/if}
	</p>
	{#if balance}
		<div class="cap" title="Today's spend against the daily cap">
			<div class="bar"><span style:width="{capUsed * 100}%"></span></div>
			<span class="micro">{usd(balance.spent_today_micros)} of {usd(balance.daily_cap_micros)} today</span>
		</div>
	{/if}
	<div class="stub">
		<Barcode value={shortID(userID)} height={20} />
	</div>
</a>

<style>
	.fare {
		display: block;
		padding: 0.875rem 1rem 0;
		text-decoration: none;
	}
	.head {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}
	.amount {
		margin: 0.25rem 0 0;
		font-size: 2.5rem;
		font-weight: 900;
		font-stretch: 75%;
		letter-spacing: -0.03em;
		line-height: 1;
		font-variant-numeric: tabular-nums;
	}
	.sub {
		margin: 0.25rem 0 0.75rem;
	}
	.cap {
		display: grid;
		gap: 4px;
		margin-bottom: 0.75rem;
	}
	.bar {
		height: 4px;
		background: rgb(0 0 0 / 0.15);
	}
	.bar span {
		display: block;
		height: 100%;
		background: var(--ink);
	}
	.stub {
		border-top: 2px dashed rgb(0 0 0 / 0.35);
		padding: 0.5rem 0 0.625rem;
	}
</style>
