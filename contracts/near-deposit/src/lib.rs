//! Adytus deposit contract.
//!
//! Users pay this one contract with their memo, and it appends each payment
//! to a log that the API reads in order (apps/api/internal/billing/near.go):
//!
//! - NEAR: call `deposit(memo)` with the NEAR attached.
//! - USDC (or any token in `tokens`): `ft_transfer_call` on the token, with
//!   this contract as receiver and the memo as `msg`.
//!
//! There is deliberately no withdraw method and no owner. Funds leave with
//! this account's own full-access key, kept offline: a plain transfer for
//! NEAR, `ft_transfer` signed by this account for tokens. The only call that
//! changes settings, `set_tokens`, also needs that key.

use near_sdk::json_types::U128;
use near_sdk::serde_json::json;
use near_sdk::store::Vector;
use near_sdk::{env, near, require, AccountId, NearToken, PanicOnDefault, PromiseOrValue};

/// Smallest NEAR payment. It more than covers the log entry's storage, which
/// the contract pays for out of its own balance.
const MIN_NEAR: NearToken = NearToken::from_millinear(10);
const MAX_MEMO: usize = 32;
/// Most entries one `deposits` call returns.
const MAX_PAGE: u64 = 100;

#[near(serializers = [borsh, json])]
#[derive(Clone)]
pub struct Deposit {
    pub memo: String,
    /// The token contract for a token payment, or null for NEAR. It is the
    /// account that called `ft_on_transfer`, so it can't be forged.
    pub token: Option<AccountId>,
    pub sender: AccountId,
    /// yoctoNEAR, or the token's base units.
    pub amount: U128,
    pub block_height: u64,
    pub timestamp_ms: u64,
}

#[near(contract_state)]
#[derive(PanicOnDefault)]
pub struct Contract {
    tokens: Vec<AccountId>,
    /// Smallest token payment, in base units, so a stream of tiny transfers
    /// can't fill storage the contract pays for.
    min_token_amount: U128,
    deposits: Vector<Deposit>,
}

#[near]
impl Contract {
    #[init]
    pub fn new(tokens: Vec<AccountId>, min_token_amount: U128) -> Self {
        Self { tokens, min_token_amount, deposits: Vector::new(b"d") }
    }

    /// Pays the attached NEAR in, credited to whoever owns `memo`.
    #[payable]
    pub fn deposit(&mut self, memo: String) {
        let amount = env::attached_deposit();
        require!(amount >= MIN_NEAR, "attach at least 0.01 NEAR");
        check_memo(&memo);
        self.log(memo, None, env::predecessor_account_id(), amount.as_yoctonear());
    }

    /// NEP-141 receiver. Panicking refuses the transfer, and the token
    /// contract refunds it; returning 0 keeps all of it.
    pub fn ft_on_transfer(&mut self, sender_id: AccountId, amount: U128, msg: String) -> PromiseOrValue<U128> {
        let token = env::predecessor_account_id();
        require!(self.tokens.contains(&token), "this token isn't accepted");
        require!(amount.0 >= self.min_token_amount.0, "amount too small");
        check_memo(&msg);
        self.log(msg, Some(token), sender_id, amount.0);
        PromiseOrValue::Value(U128(0))
    }

    /// Changes the accepted tokens. Only this account, with its own key.
    pub fn set_tokens(&mut self, tokens: Vec<AccountId>, min_token_amount: U128) {
        require!(
            env::predecessor_account_id() == env::current_account_id(),
            "only the contract account can change its tokens"
        );
        self.tokens = tokens;
        self.min_token_amount = min_token_amount;
    }

    pub fn tokens(&self) -> Vec<AccountId> {
        self.tokens.clone()
    }

    pub fn min_token_amount(&self) -> U128 {
        self.min_token_amount
    }

    pub fn deposits_count(&self) -> u64 {
        u64::from(self.deposits.len())
    }

    /// Up to `limit` (at most 100) entries, starting at `from_index`.
    pub fn deposits(&self, from_index: u64, limit: u64) -> Vec<Deposit> {
        let len = u64::from(self.deposits.len());
        let end = from_index.saturating_add(limit.min(MAX_PAGE)).min(len);
        (from_index..end).map(|i| self.deposits[i as u32].clone()).collect()
    }
}

impl Contract {
    fn log(&mut self, memo: String, token: Option<AccountId>, sender: AccountId, amount: u128) {
        let d = Deposit {
            memo,
            token,
            sender,
            amount: U128(amount),
            block_height: env::block_height(),
            timestamp_ms: env::block_timestamp_ms(),
        };
        // NEP-297 event, for explorers and indexers; the API reads the log.
        env::log_str(&format!(
            "EVENT_JSON:{}",
            json!({ "standard": "adytus-deposit", "version": "1.0.0", "event": "deposit", "data": [&d] })
        ));
        self.deposits.push(d);
    }
}

fn check_memo(memo: &str) {
    require!(
        !memo.is_empty()
            && memo.len() <= MAX_MEMO
            && memo.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit()),
        "the memo is 1 to 32 lower-case letters or digits"
    );
}

#[cfg(test)]
mod tests {
    use super::*;
    use near_sdk::test_utils::{accounts, VMContextBuilder};
    use near_sdk::testing_env;

    fn usdc() -> AccountId {
        "usdc.testnet".parse().unwrap()
    }

    fn ctx(predecessor: AccountId, deposit: NearToken) {
        testing_env!(VMContextBuilder::new()
            .current_account_id("pay.testnet".parse().unwrap())
            .predecessor_account_id(predecessor)
            .attached_deposit(deposit)
            .build());
    }

    fn contract() -> Contract {
        ctx(accounts(0), NearToken::from_near(0));
        Contract::new(vec![usdc()], U128(100_000))
    }

    #[test]
    fn logs_near_and_tokens() {
        let mut c = contract();
        ctx(accounts(1), NearToken::from_near(2));
        c.deposit("abc123".into());
        ctx(usdc(), NearToken::from_yoctonear(0));
        let kept = c.ft_on_transfer(accounts(2), U128(5_000_000), "abc123".into());
        assert!(matches!(kept, PromiseOrValue::Value(U128(0))));

        assert_eq!(c.deposits_count(), 2);
        let log = c.deposits(0, 10);
        assert_eq!(log[0].token, None);
        assert_eq!(log[0].sender, accounts(1));
        assert_eq!(log[0].amount.0, NearToken::from_near(2).as_yoctonear());
        assert_eq!(log[1].token, Some(usdc()));
        assert_eq!(log[1].sender, accounts(2));
        assert_eq!(c.deposits(1, 10).len(), 1);
        assert_eq!(c.deposits(5, 10).len(), 0);
    }

    #[test]
    #[should_panic(expected = "this token isn't accepted")]
    fn refuses_other_tokens() {
        let mut c = contract();
        ctx(accounts(3), NearToken::from_yoctonear(0));
        let _ = c.ft_on_transfer(accounts(2), U128(5_000_000), "abc".into());
    }

    #[test]
    #[should_panic(expected = "attach at least 0.01 NEAR")]
    fn refuses_tiny_near() {
        let mut c = contract();
        ctx(accounts(1), NearToken::from_millinear(1));
        c.deposit("abc".into());
    }

    #[test]
    #[should_panic(expected = "the memo is 1 to 32")]
    fn refuses_bad_memo() {
        let mut c = contract();
        ctx(accounts(1), NearToken::from_near(1));
        c.deposit("Not A Memo".into());
    }

    #[test]
    #[should_panic(expected = "only the contract account")]
    fn only_self_sets_tokens() {
        let mut c = contract();
        ctx(accounts(1), NearToken::from_yoctonear(0));
        c.set_tokens(vec![], U128(0));
    }
}
