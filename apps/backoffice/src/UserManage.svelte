<script lang="ts">
	import { api, type UserUsage } from './api.ts';

	// Credit and daily cap for one user. Both reply with the refreshed list.
	let { user, onchange }: { user: UserUsage; onchange: (users: UserUsage[]) => void } = $props();

	let tokens = $state<number | null>(null);
	let note = $state('');
	let capMode = $state<'default' | 'custom' | 'none'>('default');
	let capTokens = $state<number | null>(null);
	let busy = $state('');
	let error = $state('');
	let done = $state('');

	$effect.pre(() => {
		const own = user.own_cap_tokens;
		capMode = own === null ? 'default' : own === 0 ? 'none' : 'custom';
		capTokens = own && own > 0 ? own : null;
	});

	async function run(key: string, fn: () => Promise<{ users: UserUsage[] }>, ok: string) {
		busy = key;
		error = '';
		done = '';
		try {
			onchange((await fn()).users);
			done = ok;
		} catch (err) {
			error = (err as Error).message;
		} finally {
			busy = '';
		}
	}

	function adjust(e: SubmitEvent) {
		e.preventDefault();
		if (!tokens) return;
		const n = Math.trunc(tokens);
		if (n < 0 && !confirm(`Take ${(-n).toLocaleString()} tokens from ${user.email}?`)) return;
		void run('credit', () => api.adjustCredit(user.id, n, note.trim()), n > 0 ? `Gave ${n.toLocaleString()} tokens.` : `Took ${(-n).toLocaleString()} tokens.`).then(() => {
			if (!error) {
				tokens = null;
				note = '';
			}
		});
	}

	function saveCap(e: SubmitEvent) {
		e.preventDefault();
		const value = capMode === 'default' ? null : capMode === 'none' ? 0 : Math.trunc(capTokens ?? 0);
		if (capMode === 'custom' && !(value && value > 0)) {
			error = 'Enter a cap of at least 1 token, or pick no cap.';
			return;
		}
		void run('cap', () => api.setDailyCap(user.id, value), 'Daily cap saved.');
	}
</script>

<div class="manage">
	<form onsubmit={adjust}>
		<h4>Give or take tokens</h4>
		<p class="hint">1,000 tokens = $1. Use a minus sign to take. Shows as an adjustment in their history.</p>
		<div class="line">
			<label class="field">
				Tokens
				<input type="number" step="1" required bind:value={tokens} placeholder="e.g. 20000" />
			</label>
			<label class="field grow">
				Note (for the audit trail)
				<input required maxlength="200" bind:value={note} placeholder="e.g. Hackathon demo account" />
			</label>
			<button class="btn primary" disabled={busy !== '' || !tokens || !note.trim()}>
				{busy === 'credit' ? 'Saving…' : (tokens ?? 0) < 0 ? 'Take' : 'Give'}
			</button>
		</div>
	</form>

	<form onsubmit={saveCap}>
		<h4>Daily cap</h4>
		<p class="hint">How many tokens they can use per UTC day. Now {user.daily_cap_tokens ? user.daily_cap_tokens.toLocaleString() : 'no cap'}.</p>
		<div class="line">
			<label class="field">
				Cap
				<select bind:value={capMode}>
					<option value="default">Server default</option>
					<option value="custom">Custom</option>
					<option value="none">No cap</option>
				</select>
			</label>
			{#if capMode === 'custom'}
				<label class="field">
					Tokens per day
					<input type="number" min="1" step="1" required bind:value={capTokens} />
				</label>
			{/if}
			<button class="btn" disabled={busy !== ''}>{busy === 'cap' ? 'Saving…' : 'Save cap'}</button>
		</div>
	</form>

	{#if error}<p class="msg bad" role="alert">{error}</p>{:else if done}<p class="msg ok" role="status">{done}</p>{/if}
</div>

<style>
	.manage {
		display: grid;
		gap: 1rem;
		padding: 0.75rem 0 1rem;
		white-space: normal;
	}
	form {
		display: grid;
		gap: 0.375rem;
	}
	h4 {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 700;
	}
	.hint {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--muted);
	}
	.line {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-end;
		gap: 0.75rem;
	}
	.field input,
	.field select {
		width: 100%;
	}
	.field {
		min-width: 9rem;
	}
	.grow {
		flex: 1;
		min-width: 14rem;
	}
	.msg {
		margin: 0;
		font-size: 0.875rem;
		font-weight: 600;
	}
	.bad {
		color: var(--line-red);
	}
	.ok {
		color: var(--line-green);
	}
</style>
