<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError, type VerifyResult, type VerifyCheck } from '#lib/api.ts';
	import Wordmark from '#lib/components/Wordmark.svelte';

	// Public proof that NEAR AI's enclave produced a reply. It shows hashes,
	// signatures and the signer, never the chat.
	let result = $state<VerifyResult | null>(null);
	let error = $state('');

	$effect(() => {
		const id = page.params.id ?? '';
		result = null;
		error = '';
		api.verify(id).then(
			(r) => (result = r),
			(err) => (error = err instanceof ApiError && err.status === 404 ? 'There’s no proof for this reply.' : err instanceof ApiError ? err.message : 'The API did not answer.')
		);
	});

	const allOk = $derived(
		result?.receipts.every((r) => r.checks.signature.ok && r.checks.attested.ok && r.checks.hashes.ok) ?? false
	);
	const rows = (r: VerifyResult['receipts'][number]): [string, VerifyCheck][] => [
		['Signature valid', r.checks.signature],
		['Signer matches attested TEE key', r.checks.attested],
		['Hashes match', r.checks.hashes]
	];
	const when = (s?: string) => (s ? new Date(s).toLocaleString() : '');
</script>

<svelte:head><title>Proof · adytus</title></svelte:head>

<main>
	<header>
		<a href="/" aria-label="adytus"><Wordmark size={1} /></a>
		<span class="micro">Inference proof</span>
	</header>

	{#if error}
		<p class="error">{error}</p>
	{:else if !result}
		<p class="muted">Checking…</p>
	{:else}
		<section class="verdict" class:ok={allOk}>
			<h1>{allOk ? '✓ Verified in TEE' : '✕ Not verified'}</h1>
			<p>
				{#if allOk}
					This reply was produced inside NEAR AI's sealed enclave. The enclave's key signed the exact bytes
					sent and received, and that key is bound to a fresh Intel TDX attestation.
				{:else}
					At least one check below didn't pass.
				{/if}
			</p>
			<p class="micro">Reply {result.message_id}</p>
		</section>

		{#each result.receipts as r (r.seq)}
			<section class="call">
				<h2>
					{result.receipts.length > 1 ? `Model call ${r.seq + 1} · ` : ''}<code>{r.model}</code>
				</h2>
				<ul class="checks">
					{#each rows(r) as [name, c] (name)}
						<li class:ok={c.ok}>
							<span class="mark" aria-hidden="true">{c.ok ? '✓' : '✕'}</span>
							<span><b>{name}</b><br /><small>{c.detail}</small></span>
						</li>
					{/each}
				</ul>
				<dl>
					<dt>Signer</dt>
					<dd><code>{r.signing_address || '—'}</code></dd>
					<dt>Completion</dt>
					<dd><code>{r.completion_id}</code></dd>
					<dt>Signed</dt>
					<dd>{when(r.created_at)}</dd>
					{#if r.checks.report_fetched_at}
						<dt>Attestation fetched</dt>
						<dd>{when(r.checks.report_fetched_at)} · nonce <code>{r.checks.report_nonce?.slice(0, 16)}…</code></dd>
					{/if}
				</dl>
				<details>
					<summary>Raw receipt</summary>
					<pre>{JSON.stringify(r, null, 2)}</pre>
				</details>
			</section>
		{/each}

		<section class="diy">
			<h2>Verify it yourself</h2>
			<p>Don't trust this page? The CLI recovers the signer locally and fetches NEAR AI's attestation report directly, with its own nonce:</p>
			<pre>cd apps/api
go run ./cmd/verify {page.url.origin} {result.message_id}</pre>
			<p class="muted">
				Checked here: the signature, the hashes, and that the signer is in NEAR AI's report with a TDX quote bound to a
				fresh nonce. For the full Intel and NVIDIA certificate chains, run the report through
				<a href="https://github.com/nearai/nearai-cloud-verifier" rel="noreferrer">NEAR AI's verifier</a>.
			</p>
		</section>
	{/if}
</main>

<style>
	main {
		max-width: 46rem;
		margin: 0 auto;
		padding: 1.5rem 1rem 4rem;
		display: grid;
		gap: 1.25rem;
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}
	header a {
		text-decoration: none;
	}
	h1 {
		margin: 0 0 0.5rem;
		font-size: 1.75rem;
	}
	h2 {
		margin: 0 0 0.75rem;
		font-size: 1rem;
	}
	section {
		border: 1px solid var(--rule);
		border-radius: 6px;
		padding: 1rem 1.25rem;
		background: var(--surface);
	}
	.verdict {
		border-color: var(--line-red);
	}
	.verdict.ok {
		border-color: var(--line-green);
	}
	.verdict h1 {
		color: var(--line-red);
	}
	.verdict.ok h1 {
		color: var(--line-green);
	}
	.verdict p {
		margin: 0 0 0.5rem;
		color: var(--text-soft);
	}
	.checks {
		list-style: none;
		margin: 0 0 1rem;
		padding: 0;
		display: grid;
		gap: 0.625rem;
	}
	.checks li {
		display: flex;
		gap: 0.625rem;
		align-items: flex-start;
	}
	.mark {
		flex: none;
		width: 1.5rem;
		height: 1.5rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		font-weight: 700;
		background: var(--line-red);
		color: #fff;
	}
	.ok .mark {
		background: var(--line-green);
	}
	small {
		color: var(--muted);
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 0.25rem 1rem;
		margin: 0 0 0.75rem;
		font-size: 0.875rem;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	code,
	pre {
		font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
		font-size: 0.8125rem;
	}
	pre {
		margin: 0.5rem 0 0;
		padding: 0.75rem;
		background: var(--deep);
		border-radius: 4px;
		overflow-x: auto;
		white-space: pre;
	}
	summary {
		cursor: pointer;
		font-size: 0.875rem;
		font-weight: 600;
	}
	.diy p {
		margin: 0 0 0.5rem;
	}
	.muted {
		color: var(--muted);
		font-size: 0.875rem;
	}
</style>
