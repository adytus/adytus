<script lang="ts">
	import { usd } from '#lib/api.ts';

	let { data } = $props();
</script>

<main>
	<p class="micro">Issued by Adytus · Private LLM transit</p>
	<h1>ADYTUS</h1>

	{#if data.offline}
		<p class="micro warn">API offline. Start apps/api on :8080.</p>
	{:else}
		<section class="tickets">
			{#each data.plans as plan (plan.id)}
				<article class="ticket">
					<span class="badge">{plan.name[0]}</span>
					<h2>{plan.name}</h2>
					<p class="big">{usd(plan.credit_micros)}</p>
					<p class="micro">credit · for {usd(plan.price_micros)}</p>
				</article>
			{/each}
		</section>
		<p class="micro">Models: {data.models.join(' / ')}</p>
	{/if}
</main>

<style>
	:global(body) {
		margin: 0;
		background: #000;
		color: #d8d6d0;
		font-family: 'Helvetica Neue', Arial, sans-serif;
	}
	main {
		max-width: 48rem;
		margin: 0 auto;
		padding: 3rem 1rem;
	}
	h1 {
		font-size: clamp(3rem, 12vw, 7rem);
		font-weight: 900;
		letter-spacing: -0.06em;
		margin: 0 0 2rem;
	}
	.micro {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		opacity: 0.7;
	}
	.warn {
		color: #ffcc00;
	}
	.tickets {
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	.ticket {
		background: #c9c6be;
		color: #111;
		border-radius: 6px;
		padding: 1rem 1.25rem;
		min-width: 14rem;
	}
	.ticket h2 {
		margin: 0.5rem 0 0;
		font-weight: 800;
	}
	.big {
		font-size: 3rem;
		font-weight: 900;
		margin: 0;
		letter-spacing: -0.04em;
	}
	.badge {
		display: inline-grid;
		place-items: center;
		width: 2rem;
		height: 2rem;
		border-radius: 50%;
		background: #1f5fd6;
		color: #fff;
		font-weight: 900;
	}
</style>
