# Puddle identity

**Whose data is this, across machines?** That question needs an answer before any peer-to-peer subsystem can work. The **puddle** is that answer (ADR-014).

## The short answer

A puddle is **one Unix user's identity, shared across the envoys they use** — a single Ed25519 keypair, generated once, carried to each machine. Every swarm content subsystem ([gitback](the-swarm.md), curator, poolq, lockbox, longdrawer) is anchored to a puddle pubkey, so "this artifact is mine" and "these machines are mine" have a cryptographic meaning that doesn't depend on the server.

The private half is passphrase-wrapped at `~/.vigo-puddle/identity.wrapped`. There is **no central puddle registry** — membership is the simple fact that the same pubkey is present on N envoys.

## How an identity spreads to a second machine

**Peer-pairing**, not copying a key around:

1. On a machine that already has the puddle, `vigo swarm puddle pair` issues a 5-minute single-use code.
2. On the new machine, `vigo swarm puddle join --from <host>` claims the code over swarm mTLS and receives the wrapped key. The passphrase never leaves the destination machine.

A `session helper` holds the unwrapped key in memory while you work (idle-timeout, parent-PID watchdog), so subsystems can sign without re-prompting.

## Friendly names (ADR-022)

A puddle pubkey is unwieldy, so a puddle can claim a human name (`dan`, `acme`) — a signed claim that propagates as a trait and resolves fleet-wide. Names are **first-come and cooperative**: the server publishes all claims and arbitrates nothing, so a contested name resolves to *ambiguous* (the hex pubkey stays the proof), never to *wrong*.

## Why per-user, per-envoy keys (not one fleet key)

Compartmentalization. A compromised single envoy shouldn't decrypt a whole puddle's corpus — which is why [lockbox](the-swarm.md) deliberately keeps a *separate* per-envoy key under the puddle. The puddle is the identity; it is not itself the decryption key for everything.

## Where this shows up

- [The swarm](the-swarm.md) — what puddles authorize.
- [Set up puddle](../howto/swarm/set-up-puddle.md) — pair, join, name.
- The `/swarm/puddle` UI + [`vigocli swarm puddle`](../reference/vigocli/swarm-puddle.md) — fleet view.
