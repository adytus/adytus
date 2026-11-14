<script lang="ts">
	import { goto } from '$app/navigation';
	import { api, ApiError } from '#lib/api.ts';
	import { nearWallets, signChallenge, type NearChallenge, type NearSigned, type NearWalletOption } from '#lib/near-login.ts';
	import Alert from '#lib/components/Alert.svelte';
	import RecoveryStub from '#lib/components/RecoveryStub.svelte';
	import Wordmark from '#lib/components/Wordmark.svelte';
	import { UI_MODE } from '#lib/theme.ts';

	type Mode = 'login' | 'signup' | 'recover';
	let mode = $state<Mode>('login');
	let email = $state('');
	let password = $state('');
	let code = $state('');
	let error = $state('');
	let busy = $state(false);
	let issuedCode = $state('');

	// New accounts land in the app, which offers to add tokens.
	const codeSaved = () => goto('/chat');

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

	// Login with NEAR: the wallet proves the account, a passphrase unlocks
	// the data (it encrypts it, as a password does).
	type NearStage = 'idle' | 'pick' | 'unlock' | 'create';
	let near = $state<NearStage>('idle');
	let nearChallenge = $state<NearChallenge | null>(null);
	let nearSigned = $state<NearSigned | null>(null);
	let wallets = $state<NearWalletOption[]>([]);
	let passphrase = $state('');

	async function startNear() {
		error = '';
		busy = true;
		try {
			nearChallenge = await api.nearChallenge();
			wallets = await nearWallets(nearChallenge.network);
			near = 'pick';
		} catch (err) {
			error = err instanceof ApiError ? err.message : 'Couldn’t start Login with NEAR. Try again.';
		} finally {
			busy = false;
		}
	}

	async function pickWallet(id: string) {
		if (!nearChallenge) return;
		error = '';
		busy = true;
		try {
			nearSigned = await signChallenge(id, nearChallenge);
			passphrase = '';
			near = 'unlock';
		} catch (err) {
			error = err instanceof Error && err.message ? err.message : 'The wallet didn’t sign. Try again.';
		} finally {
			busy = false;
		}
	}

	async function submitNear(e: SubmitEvent) {
		e.preventDefault();
		if (!nearSigned || !nearChallenge) return;
		error = '';
		busy = true;
		try {
			const r = await api.nearLogin(nearSigned, nearChallenge.nonce, passphrase, near === 'create');
			if (r.recovery_code) issuedCode = r.recovery_code;
			else await goto('/chat');
		} catch (err) {
			if (err instanceof ApiError && err.status === 404 && near === 'unlock') {
				// First time with this account: choose a passphrase instead.
				near = 'create';
				passphrase = '';
			} else if (err instanceof ApiError && /expired|already used/.test(err.message)) {
				cancelNear();
				error = err.message;
			} else {
				error = err instanceof ApiError ? err.message : 'The server could not be reached. Try again.';
			}
		} finally {
			busy = false;
		}
	}

	function cancelNear() {
		near = 'idle';
		nearSigned = null;
		nearChallenge = null;
		passphrase = '';
		error = '';
	}
</script>

<main>
	<section class="masthead">
		<h1><Wordmark size={UI_MODE === 2 ? 4.5 : 1} /></h1>
		<p class="lede">
			A private line to frontier models. Chats and files are encrypted with a key only your
			password opens, and every reply is metered to the token.
		</p>
	</section>

	{#if issuedCode}
		<RecoveryStub code={issuedCode} onclose={codeSaved} />
	{:else if near !== 'idle'}
		<form class="ticket pass" onsubmit={submitNear}>
			<div class="fields">
				{#if near === 'pick'}
					<h2>Login with NEAR</h2>
					<p class="hint">Pick your wallet. It asks you to sign a one-time message: no transaction, no fee.</p>
					<div class="wallets">
						{#each wallets as w (w.id)}
							<button type="button" class="wallet" disabled={busy} onclick={() => pickWallet(w.id)}>
								{#if w.icon}<img src={w.icon} alt="" />{/if}
								{w.name}
							</button>
						{:else}
							<p class="hint">No NEAR wallets are available in this browser.</p>
						{/each}
					</div>
				{:else}
					<h2>{near === 'create' ? 'Choose a passphrase' : 'Unlock'}</h2>
					<p class="hint">
						Signed in as <strong>{nearSigned?.account_id}</strong>.
						{near === 'create'
							? 'This is a new Adytus account. The passphrase encrypts your chats and files, so nobody else can read them, us included. At least 12 characters; it can’t be reset by email, so keep the recovery code you’re shown next.'
							: 'Enter your passphrase to open your chats.'}
					</p>
					<label class="field">
						Passphrase
						<input
							type="password"
							autocomplete={near === 'create' ? 'new-password' : 'current-password'}
							required
							minlength={near === 'create' ? 12 : undefined}
							bind:value={passphrase}
						/>
					</label>
				{/if}
				{#if error}
					<Alert title="Login with NEAR"><p>{error}</p></Alert>
				{/if}
				<div class="row">
					{#if near !== 'pick'}
						<button class="btn primary" disabled={busy}>
							{busy ? 'Working…' : near === 'create' ? 'Create account' : 'Unlock'}
						</button>
					{:else if busy}
						<span class="hint">Waiting for the wallet…</span>
					{/if}
					<button type="button" class="link" onclick={cancelNear}>Back</button>
				</div>
			</div>
		</form>
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
					{mode === 'recover' ? 'Email or NEAR account' : 'Email'}
					<input
						type={mode === 'recover' ? 'text' : 'email'}
						autocomplete={mode === 'recover' ? 'username' : 'email'}
						required
						bind:value={email}
					/>
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
				{#if error}
					<Alert title={mode === 'login' ? "Couldn't sign in" : mode === 'signup' ? "Couldn't create the account" : "Couldn't reset the password"}>
						<p>{error}</p>
					</Alert>
				{/if}
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
				{#if mode !== 'recover'}
					<div class="or"><span>or</span></div>
					<button type="button" class="btn near" disabled={busy} onclick={startNear}>Login with NEAR</button>
				{/if}
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
	h1 {
		margin: 1.5rem 0 1rem;
		font-size: clamp(4rem, 13vw, 9.5rem);
		font-weight: 900;
		font-stretch: 70%;
		letter-spacing: -0.04em;
		line-height: 0.85;
		color: var(--headline);
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
	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}
	.or {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		font-size: 0.8125rem;
		opacity: 0.6;
	}
	.or::before,
	.or::after {
		content: '';
		flex: 1;
		border-top: 1px solid currentColor;
		opacity: 0.4;
	}
	.btn.near {
		width: 100%;
	}
	.wallets {
		display: grid;
		gap: 0.5rem;
	}
	.wallet {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem 0.875rem;
		border: 1px solid var(--rule);
		border-radius: 12px;
		background: transparent;
		color: inherit;
		font: inherit;
		font-weight: 600;
		text-align: left;
		cursor: pointer;
	}
	.wallet:hover:not(:disabled) {
		background: var(--surface-2);
	}
	.wallet img {
		width: 1.75rem;
		height: 1.75rem;
		border-radius: 6px;
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
	@media (max-width: 760px) {
		main {
			grid-template-columns: 1fr;
			gap: 2rem;
			padding: 2rem 1rem;
			align-content: start;
		}
	}
</style>
