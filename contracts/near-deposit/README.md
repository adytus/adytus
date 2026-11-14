# near-deposit

The contract users pay when they top up with NEAR or USDC on NEAR. It logs each payment with the payer's memo, and the API reads that log (`apps/api/internal/billing/near.go`). It has no withdraw method and no owner. Money leaves only with the contract account's own key, so keep that key offline.

## Quick deploy (Windows)

`deploy.ps1` runs every step below, skipping any already done: it gets the NEAR CLI, builds the WASM if it's missing (with Docker), checks the USDC token, creates the account from the testnet faucet, deploys and initialises the contract, and registers it with USDC.

```powershell
cd contracts\near-deposit
powershell -ExecutionPolicy Bypass -File .\deploy.ps1 -Account adytus-pay.testnet -DryRun   # show what it would do
powershell -ExecutionPolicy Bypass -File .\deploy.ps1 -Account adytus-pay.testnet           # do it
```

Add `-Network mainnet` for mainnet (create and fund the account yourself first), `-LegacyKeychain` to keep keys in `~/.near-credentials`, or `-Redeploy` to upload a new build to an existing contract.

## Build

```sh
cargo install cargo-near
cargo near build non-reproducible-wasm   # → target/near/near_deposit.wasm
cargo test                               # unit tests, on the host
```

## Deploy (mainnet; use `testnet` and testnet accounts otherwise)

The commands below use [near-cli-rs](https://github.com/near/near-cli-rs). `pay.adytus.near` stands for whatever account you choose.

```sh
# 1. An account for the contract. Its key is the only way to move funds out.
near account create-account fund-myself pay.adytus.near '3 NEAR' autogenerate-new-keypair save-to-keychain sign-as adytus.near network-config mainnet sign-with-keychain send

# 2. Deploy and initialise. tokens: native USDC; min_token_amount: 0.10 USDC.
near contract deploy pay.adytus.near use-file target/near/near_deposit.wasm \
  with-init-call new json-args '{"tokens":["17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1"],"min_token_amount":"100000"}' \
  prepaid-gas '30.0 Tgas' attached-deposit '0 NEAR' network-config mainnet sign-with-keychain send

# 3. Register the contract with USDC, or every USDC payment bounces.
near contract call-function as-transaction 17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1 storage_deposit \
  json-args '{"account_id":"pay.adytus.near","registration_only":true}' \
  prepaid-gas '30.0 Tgas' attached-deposit '0.00125 NEAR' sign-as pay.adytus.near network-config mainnet sign-with-keychain send
```

Then, in the backoffice, open **Payments → NEAR**, enter the contract account, and run **Test**. Every check should pass before you switch it on.

## Moving funds out

Leave about 1 NEAR on the account: the log's storage is paid from its balance.

```sh
near tokens pay.adytus.near send-near treasury.near '10 NEAR' network-config mainnet sign-with-keychain send

near contract call-function as-transaction 17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1 ft_transfer \
  json-args '{"receiver_id":"treasury.near","amount":"5000000"}' \
  prepaid-gas '30.0 Tgas' attached-deposit '1 yoctoNEAR' sign-as pay.adytus.near network-config mainnet sign-with-keychain send
```

## Changing tokens

```sh
near contract call-function as-transaction pay.adytus.near set_tokens \
  json-args '{"tokens":["…"],"min_token_amount":"100000"}' \
  prepaid-gas '30.0 Tgas' attached-deposit '0 NEAR' sign-as pay.adytus.near network-config mainnet sign-with-keychain send
```

The backoffice's USDC setting has to name a token this contract accepts, and that token must have 6 decimals and be worth $1, because the API credits its base units as micro-USD.
