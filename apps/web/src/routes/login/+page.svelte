<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, ApiError } from '#lib/api.ts';
	import RecoveryStub from '#lib/components/RecoveryStub.svelte';
	import RouteBadge from '#lib/components/RouteBadge.svelte';
	import Barcode from '#lib/components/Barcode.svelte';

	type Mode = 'login' | 'signup' | 'recover';
	let mode = $state<Mode>('login');
	let email = $state('');
	let password = $state('');
	let code = $state('');
	let error = $state('');
	let busy = $state(false);
	let issuedCode = $state('');

	const titles: Record<Mode, string> = {
		login: 'Sign in',
		signup: 'Create account',
		recover: 'Reset password'
	};

	async function submit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		busy = true;
		try {
			if (mode === 'login') {
				await api.login(email, password);
				await goto('/chat');
			} else if (mode === 'signup') {
				issuedCode = (await api.signup(email, password)).recovery_code;
			} else {
				issuedCode = (await api.recover(email, code, password)).recovery_code;
			}
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'The server could not be reached. Try again.';
		} finally {
			busy = false;
		}
	}

	function switchTo(m: Mode) {
		mode = m;
		error = '';
	}
</script>

<main>
	<section class="masthead">
		<div class="route" aria-hidden="true">
			<RouteBadge line="blue">A</RouteBadge>
			<span class="arrow"></span>
			<RouteBadge line="yellow">D</RouteBadge>
			<span class="arrow"></span>
			<RouteBadge line="red">Y</RouteBadge>
			<span class="arrow"></span>
			<RouteBadge line="purple">T</RouteBadge>
		</div>
		<h1>ADYTUS</h1>
		<p class="lede">
			A private line to frontier models. Chats and files are encrypted with a key only your
			password opens, and every reply is metered to the token.
		</p>
	</section>

	{#if issuedCode}
		<RecoveryStub code={issuedCode} onclose={() => goto('/chat')} />
	{:else}
		<form class="ticket pass" onsubmit={submit}>
			<div class="tabs" role="tablist">
				<button type="button" role="tab" aria-selected={mode === 'login'} onclick={() => switchTo('login')}>
					Sign in
				</button>
				<button type="button" role="tab" aria-selected={mode === 'signup'} onclick={() => switchTo('signup')}>
					Create account
				</button>
			</div>

			<div class="fields">
				<h2>{titles[mode]}</h2>
				{#if mode === 'recover'}
					<p class="hint">Enter the recovery code you saved at sign-up and choose a new password.</p>
				{/if}
				<label class="field">
					Email
					<input type="email" autocomplete="email" required bind:value={email} />
				</label>
				{#if mode === 'recover'}
					<label class="field">
						Recovery code
						<input autocomplete="off" spellcheck="false" required bind:value={code} placeholder="XXXX-XXXX-…" />
					</label>
				{/if}
				<label class="field">
					{mode === 'login' ? 'Password' : 'New password'}
					<input
						type="password"
						autocomplete={mode === 'login' ? 'current-password' : 'new-password'}
						required
						minlength={mode === 'login' ? undefined : 12}
						bind:value={password}
					/>
				</label>
				{#if mode !== 'login'}
					<p class="hint">At least 12 characters. It encrypts your data, so it can't be reset by email.</p>
				{/if}
				{#if error}<p class="error" role="alert">{error}</p>{/if}
				<div class="row">
					<button class="btn primary" disabled={busy}>
						{busy ? 'Working…' : titles[mode]}
					</button>
					{#if mode === 'login'}
						<button type="button" class="link" onclick={() => switchTo('recover')}>Forgot password?</button>
					{:else if mode === 'recover'}
						<button type="button" class="link" onclick={() => switchTo('login')}>Back to sign in</button>
					{/if}
				</div>
			</div>

			<div class="perf">
				<span class="micro">Issued by Adytus · Valid on all lines</span>
				<Barcode value="ADY-0001" height={18} />
			</div>
		</form>
	{/if}
</main>

<style>
	main {
		min-height: 100dvh;
		display: grid;
		grid-template-columns: minmax(0, 1.1fr) minmax(0, 26rem);
		gap: 3rem;
		align-items: center;
		max-width: 68rem;
		margin: 0 auto;
		padding: 3rem 1.5rem;
	}
	.route {
		display: flex;
		align-items: center;
	}
	.arrow {
		width: 1.25rem;
		height: 4px;
		background: var(--stock);
	}
	h1 {
		margin: 1.5rem 0 1rem;
		font-size: clamp(4rem, 13vw, 9.5rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.04em;
		line-height: 0.85;
		color: var(--stock);
	}
	.lede {
		max-width: 34ch;
		font-size: 1.125rem;
		line-height: 1.5;
		color: var(--muted);
		margin: 0;
	}
	.pass {
		overflow: hidden;
	}
	.tabs {
		display: grid;
		grid-template-columns: 1fr 1fr;
		background: var(--ink);
	}
	.tabs button {
		font: inherit;
		font-weight: 700;
		font-size: 0.875rem;
		padding: 0.75rem;
		border: 0;
		background: transparent;
		color: var(--muted);
		cursor: pointer;
	}
	.tabs button[aria-selected='true'] {
		background: var(--stock);
		color: var(--ink);
	}
	.fields {
		display: grid;
		gap: 0.875rem;
		padding: 1.25rem;
	}
	h2 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 800;
		font-stretch: 85%;
		letter-spacing: -0.02em;
	}
	.hint {
		margin: -0.25rem 0 0;
		font-size: 0.8125rem;
		line-height: 1.4;
		opacity: 0.75;
	}
	.error {
		margin: 0;
	}
	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}
	.link {
		font: inherit;
		font-size: 0.875rem;
		background: none;
		border: 0;
		padding: 0;
		color: inherit;
		text-decoration: underline;
		cursor: pointer;
	}
	.perf {
		display: flex;
		justify-content: space-between;
		align-items: end;
		gap: 1rem;
		padding: 0.75rem 1.25rem;
		border-top: 2px dashed rgb(0 0 0 / 0.35);
	}
	@media (max-width: 760px) {
		main {
			grid-template-columns: 1fr;
			gap: 2rem;
			padding: 2rem 1rem;
			align-content: start;
		}
	}
</style>
