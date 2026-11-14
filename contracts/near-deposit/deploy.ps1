<#
.SYNOPSIS
  Deploys the Adytus deposit contract (contracts/near-deposit) to NEAR.

.DESCRIPTION
  Does every step in this folder's README, skipping any already done, so it
  is safe to run again:
    1. Gets the NEAR CLI (near-cli-rs) if it isn't on PATH.
    2. Uses the built WASM, or builds it with Docker if it's missing.
    3. Checks the USDC token is a 6-decimal token on the chosen network.
    4. Creates the contract account from the faucet (testnet only).
    5. Deploys and initialises the contract.
    6. Registers the contract with USDC, so USDC payments don't bounce.
    7. Reads the contract back and prints what to enter in the backoffice.

  The account's key is the only way to move funds out of the contract. It is
  saved to your OS keychain (or ~/.near-credentials with -LegacyKeychain).

.EXAMPLE
  .\deploy.ps1 -Account adytus-pay.testnet

.EXAMPLE
  .\deploy.ps1 -Account pay.adytus.near -Network mainnet
#>
[CmdletBinding()]
param(
    # The account to deploy to, e.g. adytus-pay.testnet.
    [Parameter(Mandatory = $true)]
    [string]$Account,

    [ValidateSet('testnet', 'mainnet')]
    [string]$Network = 'testnet',

    # The USDC token. Defaults to Circle's USDC on the chosen network.
    [string]$Usdc = '',

    # Smallest USDC payment the contract takes, in base units (100000 = $0.10).
    [string]$MinTokenAmount = '100000',

    # Store keys in ~/.near-credentials instead of the OS keychain.
    [switch]$LegacyKeychain,

    # Upload the WASM again to an account that already has the contract,
    # keeping its state (no init call).
    [switch]$Redeploy,

    # Print the commands instead of running anything that changes the chain.
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3

$CliVersion = '0.31.1'
$UsdcDefaults = @{
    mainnet = '17208628f84f5d6ad33f0da3bbbeb27ffcb398eac501a31bd6ad2011e36133a1'
    testnet = '3e2210e1184b45b64c8a434c0a7e7b23cc04ea7eb7a6c3c32520d03d4afcb8af'
}
$Here = $PSScriptRoot
$Wasm = Join-Path $Here 'target\near\near_deposit.wasm'
$Account = $Account.Trim().ToLower()
if (-not $Usdc) { $Usdc = $UsdcDefaults[$Network] }
$Sign = if ($LegacyKeychain) { 'sign-with-legacy-keychain' } else { 'sign-with-keychain' }
$Save = if ($LegacyKeychain) { 'save-to-legacy-keychain' } else { 'save-to-keychain' }

function Step([string]$Text) { Write-Host ''; Write-Host "==> $Text" -ForegroundColor Cyan }
function Ok([string]$Text) { Write-Host "    ok: $Text" -ForegroundColor Green }
function Fail([string]$Text) { Write-Host ''; Write-Host "error: $Text" -ForegroundColor Red; exit 1 }

function To-B64([string]$Json) { [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($Json)) }

# Quote an argument for a native command line (Start-Process joins them).
function Quote-Arg([string]$A) {
    if ($A -notmatch '[\s"]') { return $A }
    return '"' + ($A -replace '"', '\"') + '"'
}

# Read runs a read-only CLI call and returns @{ Code; Out } with stdout only.
# Start-Process keeps the CLI's progress output (stderr) away from PowerShell,
# which in 5.1 would otherwise treat it as an error.
function Read-Near([string[]]$NearArgs) {
    $o = [IO.Path]::GetTempFileName()
    $e = [IO.Path]::GetTempFileName()
    try {
        $p = Start-Process -FilePath $script:Near -ArgumentList (($NearArgs | ForEach-Object { Quote-Arg $_ }) -join ' ') `
            -NoNewWindow -Wait -PassThru -RedirectStandardOutput $o -RedirectStandardError $e
        return @{ Code = $p.ExitCode; Out = (Get-Content $o -Raw); Err = (Get-Content $e -Raw) }
    } finally {
        Remove-Item $o, $e -ErrorAction SilentlyContinue
    }
}

# Send runs a CLI call that changes the chain, showing its output.
function Send-Near([string[]]$NearArgs) {
    $line = 'near ' + (($NearArgs | ForEach-Object { Quote-Arg $_ }) -join ' ')
    if ($DryRun) { Write-Host "    would run: $line" -ForegroundColor Yellow; return $false }
    $p = Start-Process -FilePath $script:Near -ArgumentList (($NearArgs | ForEach-Object { Quote-Arg $_ }) -join ' ') -NoNewWindow -Wait -PassThru
    if ($p.ExitCode -ne 0) { Fail "this command failed (exit $($p.ExitCode)):`n    $line" }
    return $true
}

function View([string]$Contract, [string]$Method, [string]$Json = '{}') {
    Read-Near @('contract', 'call-function', 'as-read-only', $Contract, $Method, 'base64-args', (To-B64 $Json), 'network-config', $Network, 'now')
}

# --- 1. NEAR CLI --------------------------------------------------------------
Step 'NEAR CLI'
$Near = $null
$onPath = Get-Command near -ErrorAction SilentlyContinue
if ($onPath -and $onPath.Source -like '*.exe') { $Near = $onPath.Source }
$local = Join-Path $Here ".tools\near-cli-$CliVersion\near.exe"
if (-not $Near -and (Test-Path $local)) { $Near = $local }
if (-not $Near) {
    # npm's near-cli-rs package downloads this same file in an install script,
    # which recent npm skips by default; fetching it directly avoids that.
    $dir = Split-Path $local
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    $tgz = Join-Path $dir 'near-cli.tar.gz'
    $url = "https://github.com/near/near-cli-rs/releases/download/v$CliVersion/near-cli-rs-x86_64-pc-windows-msvc.tar.gz"
    Write-Host "    downloading $url"
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $url -OutFile $tgz -UseBasicParsing
    & "$env:SystemRoot\System32\tar.exe" -xzf $tgz -C $dir
    if ($LASTEXITCODE -ne 0) { Fail 'could not unpack the NEAR CLI' }
    $found = Get-ChildItem -Path $dir -Recurse -Filter 'near.exe' | Select-Object -First 1
    if (-not $found) { Fail 'the NEAR CLI download had no near.exe' }
    if ($found.FullName -ne $local) { Move-Item $found.FullName $local -Force }
    Remove-Item $tgz -ErrorAction SilentlyContinue
    $Near = $local
}
$ver = Read-Near @('--version')
Ok "$($ver.Out.Trim()) at $Near"

# --- 2. WASM ------------------------------------------------------------------
Step 'Contract WASM'
if (-not (Test-Path $Wasm)) {
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        Fail "no $Wasm, and Docker isn't available to build it. Build it with: cargo near build non-reproducible-wasm"
    }
    Write-Host '    not built yet; building with Docker (a few minutes the first time)'
    $src = (Resolve-Path $Here).Path
    $build = 'set -e; rustup target add wasm32-unknown-unknown >/dev/null 2>&1; ' +
        'if [ ! -x /opt/cargobin/bin/cargo-near ]; then cargo install cargo-near --locked --root /opt/cargobin; fi; ' +
        '/opt/cargobin/bin/cargo-near near build non-reproducible-wasm'
    & docker run --rm -v "${src}:/src" -v adytus-cargo:/usr/local/cargo/registry -v adytus-cargo-bin:/opt/cargobin -w /src rust:1 sh -c $build
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $Wasm)) { Fail 'the Docker build failed' }
}
$hash = (Get-FileHash $Wasm -Algorithm SHA256).Hash.ToLower()
Ok ("{0} ({1:N0} bytes, sha256 {2})" -f $Wasm, (Get-Item $Wasm).Length, $hash)

# --- 3. USDC ------------------------------------------------------------------
Step "USDC token on $Network"
$meta = View $Usdc 'ft_metadata'
if ($meta.Code -ne 0) { Fail "couldn't read $Usdc on $Network. Is it a token on this network?`n$($meta.Err)" }
$m = $meta.Out | ConvertFrom-Json
if ($m.decimals -ne 6) { Fail "$Usdc has $($m.decimals) decimals; the API credits base units as micro-USD, so it needs 6" }
Ok "$($m.symbol), $($m.decimals) decimals ($Usdc)"

# --- 4. Account ---------------------------------------------------------------
Step "Account $Account"
$acct = Read-Near @('account', 'view-account-summary', $Account, 'network-config', $Network, 'now')
if ($acct.Code -eq 0) {
    Ok 'exists'
} elseif ($Network -eq 'testnet') {
    Write-Host '    not found; creating it with the testnet faucet'
    [void](Send-Near @('account', 'create-account', 'sponsor-by-faucet-service', $Account, 'autogenerate-new-keypair', $Save, 'network-config', $Network, 'create'))
    if (-not $DryRun) {
        Start-Sleep -Seconds 3
        if ((Read-Near @('account', 'view-account-summary', $Account, 'network-config', $Network, 'now')).Code -ne 0) {
            Fail "the faucet didn't create $Account. Pick another name, or create it at https://testnet.mynearwallet.com"
        }
        Ok 'created; its key is in your keychain'
    }
} else {
    Fail "$Account doesn't exist on mainnet. Create and fund it (about 3 NEAR) with your own wallet, then run this again"
}

# --- 5. Deploy ----------------------------------------------------------------
Step 'Deploy'
$count = View $Account 'deposits_count'
if ($count.Code -eq 0 -and -not $Redeploy) {
    Ok "the contract is already deployed ($($count.Out.Trim()) payments logged); use -Redeploy to upload it again"
} elseif ($count.Code -eq 0) {
    if (Send-Near @('contract', 'deploy', $Account, 'use-file', $Wasm, 'without-init-call', 'network-config', $Network, $Sign, 'send')) {
        Ok 'uploaded again; state kept'
    }
} else {
    $init = '{"tokens":["' + $Usdc + '"],"min_token_amount":"' + $MinTokenAmount + '"}'
    if (Send-Near @('contract', 'deploy', $Account, 'use-file', $Wasm, 'with-init-call', 'new', 'base64-args', (To-B64 $init),
        'prepaid-gas', '30.0 Tgas', 'attached-deposit', '0 NEAR', 'network-config', $Network, $Sign, 'send')) {
        Ok "deployed and initialised: accepts $Usdc, minimum $MinTokenAmount base units"
    }
}

# --- 6. USDC storage ------------------------------------------------------------
Step 'Register with USDC'
$reg = View $Usdc 'storage_balance_of' ('{"account_id":"' + $Account + '"}')
if ($reg.Code -eq 0 -and $reg.Out.Trim() -ne 'null' -and $reg.Out.Trim() -ne '') {
    Ok 'already registered'
} else {
    $sd = '{"account_id":"' + $Account + '","registration_only":true}'
    if (Send-Near @('contract', 'call-function', 'as-transaction', $Usdc, 'storage_deposit', 'base64-args', (To-B64 $sd),
        'prepaid-gas', '30.0 Tgas', 'attached-deposit', '0.00125 NEAR', 'sign-as', $Account, 'network-config', $Network, $Sign, 'send')) {
        Ok 'registered'
    }
}

# --- 7. Check -------------------------------------------------------------------
Step 'Check'
if ($DryRun) {
    Write-Host '    dry run: nothing was changed' -ForegroundColor Yellow
} else {
    $c = View $Account 'deposits_count'
    $t = View $Account 'tokens'
    if ($c.Code -ne 0 -or $t.Code -ne 0) { Fail "the contract doesn't answer yet; wait a few seconds and run this again" }
    $tokens = ($t.Out | ConvertFrom-Json)
    if ($tokens -notcontains $Usdc) { Fail "the contract doesn't accept $Usdc (it takes: $($tokens -join ', ')). See 'Changing tokens' in the README" }
    Ok "$($c.Out.Trim()) payments logged; accepts $($tokens -join ', ')"
}

$explorer = if ($Network -eq 'testnet') { 'https://testnet.nearblocks.io' } else { 'https://nearblocks.io' }
Write-Host ''
Write-Host 'Done. In the backoffice, Payments -> NEAR:' -ForegroundColor Green
Write-Host "    Network           $Network"
Write-Host "    Deposit contract  $Account"
Write-Host "    USDC token        $Usdc"
Write-Host '  Save, then Test these settings, then Switch on.'
Write-Host "  Explorer: $explorer/address/$Account"
