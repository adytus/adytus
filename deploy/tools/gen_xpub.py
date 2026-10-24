#!/usr/bin/env python3
"""Generate the wallet behind BASE_XPUB, or re-derive it from a seed phrase.

    pip install bip_utils
    python3 gen_xpub.py            # new 24-word seed phrase, its xpub, sample addresses
    python3 gen_xpub.py --check    # re-enter an existing phrase to see its xpub and addresses

Run it on a machine you trust, ideally offline. The seed phrase is the only
thing that can spend the money users pay; write it down on paper, keep it off
servers, cloud notes and chat. Only the xpub goes into BASE_XPUB: it can derive
deposit addresses but cannot spend from them.

Deposit address i is m/44'/60'/0'/0/i. The API gives users indices 1, 2, 3...
(index 0 is never used), so a wallet that imports this phrase sees user N's
address as its Nth address on the standard Ethereum path, counting from 0.
"""
import getpass
import sys

from bip_utils import (
    Bip39MnemonicGenerator,
    Bip39MnemonicValidator,
    Bip39SeedGenerator,
    Bip39WordsNum,
    Bip44,
    Bip44Changes,
    Bip44Coins,
)

SAMPLE = 5


def derive(mnemonic: str):
    seed = Bip39SeedGenerator(mnemonic).Generate()
    chain = (
        Bip44.FromSeed(seed, Bip44Coins.ETHEREUM)
        .Purpose()
        .Coin()
        .Account(0)
        .Change(Bip44Changes.CHAIN_EXT)
    )
    return chain


def show(mnemonic: str, reveal: bool) -> None:
    chain = derive(mnemonic)
    if reveal:
        print("\nSEED PHRASE (write it down, it is shown once):\n")
        words = mnemonic.split()
        for i in range(0, len(words), 4):
            print("  " + "  ".join(f"{i + j + 1:2}. {w:<10}" for j, w in enumerate(words[i : i + 4])))
    print("\nBASE_XPUB (safe to put on the server):\n")
    print("  " + chain.PublicKey().ToExtended())
    print("\nFirst deposit addresses (user 1 gets index 1):\n")
    for i in range(SAMPLE):
        print(f"  index {i}: {chain.AddressIndex(i).PublicKey().ToAddress()}")
    print()


def main() -> None:
    if "--check" in sys.argv[1:]:
        phrase = " ".join(getpass.getpass("Seed phrase: ").split())
        if not Bip39MnemonicValidator().IsValid(phrase):
            sys.exit("That is not a valid BIP-39 seed phrase.")
        show(phrase, reveal=False)
        return
    mnemonic = str(Bip39MnemonicGenerator().FromWordsNumber(Bip39WordsNum.WORDS_NUM_24))
    show(mnemonic, reveal=True)
    print("Anyone with the seed phrase can take all deposited funds. Never put it in Railway.\n")


if __name__ == "__main__":
    main()
