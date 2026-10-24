<script lang="ts">
	import { tokenCount, type Balance } from '../api.ts';
	import { app } from '../state.svelte.ts';

	// A slim fare card: what's left, and how much of what was bought is used.
	let { balance }: { balance: Balance | null } = $props();

	const used = $derived(
		balance && balance.purchased_tokens > 0 ? Math.min(1, balance.used_tokens / balance.purchased_tokens) : 0
	);
</script>

<div class="fare ticket">
<a class="info" href="/account" aria-label="Balance: {balance ? tokenCount(balance.available_tokens) : 'loading'} tokens left. Open account">
	<div class="row">
		<span class="left">{balance ? tokenCount(balance.available_tokens) : '—'}</span>
		<span class="label">tokens left</span>
		{#if balance && balance.held_tokens > 0}
			<span class="held" title="Reserved for a reply in progress">{tokenCount(balance.held_tokens)} held</span>
		{/if}
	</div>
	{#if balance && balance.purchased_tokens > 0}
		<div class="bar" aria-hidden="true"><span style:width="{used * 100}%"></span></div>
		<p class="sub">{tokenCount(balance.used_tokens)} used of {tokenCount(balance.purchased_tokens)}</p>
	{:else}
		<p class="sub">No tokens yet. Add some to start chatting.</p>
	{/if}
</a>
<button class="btn add" type="button" onclick={() => app.openTopUp()}>Add tokens</button>
</div>

<style>
	.fare {
		display: grid;
		gap: 0.625rem;
		padding: 0.625rem 0.75rem;
		border-left: 6px solid var(--line-yellow);
	}
	.info {
		display: grid;
		gap: 0.375rem;
		text-decoration: none;
		color: inherit;
	}
	.add {
		width: 100%;
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
