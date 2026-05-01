---
title: "ADR-017: No fleet-level revoke"
---

# ADR-017: No fleet-level revoke

**Status:** Accepted
**Date:** 2026-04-30

## Context

ADR-016 introduced fleet-aware membership for cryptsync and, in passing, asserted that envoy eviction would happen "via self-fleet's lock-and-revoke flow." That sentence pointed at a primitive that does not exist — ADR-014's self-fleet ships no revocation verb in v1, and the question of whether one should be built was deferred. Slices 1–4 of ADR-016 shipped without it, and the gap surfaced when working through a concrete stolen-laptop scenario.

The natural-sounding feature would be `vigo swarm self-fleet revoke <host>`: founder issues a signed envelope, peers gossip it, receivers filter the revoked envoy's per-envoy age pubkey out of cryptsync recipient resolution. The mechanics map cleanly onto the existing `cryptsync/leaves.rs` envelope + gossip + 90-day-reap pattern. It looks straightforward.

It is not actually solving the problem an admin has.

## Decision

**Fleet-level revoke is out of scope. Self-fleet ships no `revoke` verb. Cryptsync's existing per-envoy `leave` envelope is the only eviction primitive.**

The reasoning splits along the two scenarios that motivate eviction:

### Adversarial removal (compromised envoy)

In the stolen-laptop case, the thief gets a copy of the **fleet keypair itself** — every envoy in the fleet holds the same private key, that's how self-fleet works. Even with a wrapped blob and a passphrase, the keypair is now offline-attackable on a compromised device. With the fleet keypair in adversarial hands, the attacker can:

- Issue fleet-membership certificates for arbitrary new age pubkeys, re-entering the fleet under a different identity.
- Sign anything other envoys treat as fleet-attested.
- Forge any future signing layer added on top — including revoke envelopes themselves, even with founder asymmetry, because the *membership* key is the layer being attacked.

A "revoke laptop's age pubkey" command leaves the actual root compromise — the shared fleet keypair on a hostile device — entirely unaddressed. The only correct response is to retire the fleet keypair: re-init the fleet, accept the blast radius (gitmirror project_ids re-anchor, cryptsync fleet-mode wrap re-derives, surviving envoys re-pair), and rotate every other credential the laptop touched. Revoke would be a placebo.

### Cooperative removal (clean decommission)

When an envoy is being decommissioned willingly — old laptop being sold after wipe, server being repurposed, fleet shrinking — the departing envoy can sign a `cryptsync leave` envelope with its own age key today (`agent/src/swarm/cryptsync/leaves.rs`). The envelope gossips to live peers, gets persisted under 90-day reap, and `regenerate_recipients()` filters the departing envoy's pubkey out of the recipient set on every other envoy. This works without any fleet-level primitive.

The departing envoy stopping its self-fleet announces also lets it age out of LAN-discovered peer tables naturally. For receivers cross-subnet-pinned, `vigo swarm cryptsync forget <host>` clears the TOFU pin.

The cooperative case has its mechanism. Building a second one at the fleet layer would be redundant.

## Alternatives Considered

**Founder-asymmetric revoke (admin keypair distinct from fleet keypair).** The cleanest version: founder generates two keypairs at `init`, distributes only the membership keypair to joining envoys, retains an admin-only signing key. Revoke envelopes must be admin-signed. Receivers verify against an admin pubkey embedded in the wrapped distribution blob. Rejected — solves only a fraction of the threat (a current member can't forge a revoke), but does not address the stolen-fleet-key problem at all. The thief still has the membership key and can keep participating under new age pubkeys until the membership key itself is retired. Adds primitive complexity, does not change the runbook.

**Flat-trust revoke (any current member can revoke any other).** Cheaper to implement, no admin-keypair primitive needed. Rejected — invites a footgun in exactly the scenario it claims to address: the stolen laptop is a current member, can sign a counter-revoke against the founder, and gossip wins are determined by who's online longer. The mechanism makes adversarial outcomes worse than no mechanism.

**Cert-non-renewal as eviction.** Fleet-membership certificates are 30-day-bounded. Don't renew the evicted envoy's cert; it falls out of recipients when its cert expires. Rejected — each envoy issues its own fleet-cert by signing with its locally-held fleet keypair, so an evicted envoy can keep generating fresh self-certs indefinitely. Non-renewal isn't a thing the founder can withhold.

**Build revoke anyway, document its limits.** Rejected — the runbook for "envoy compromised" is the same with or without the revoke verb (re-init the fleet). Shipping a verb whose advertised use case it doesn't actually fulfill creates a confusion vector worse than the missing feature, and bakes in an attractive-nuisance API that future ADRs would have to either deprecate or extend toward something it can't reach.

## Consequences

**Code:**
- No new self-fleet verb.
- ADR-016's "Eviction is fleet-level" sentence is superseded by this ADR. The cryptsync-leave envelope path remains the eviction mechanism.
- `agent/src/swarm/self_fleet/cli.rs` `print_usage()` does not list a revoke verb. No need to add explanatory text — absence is the right signal.

**Documentation:**
- `docs/concepts/cryptsync.md` — the eviction section, if/when written, points at `vigo swarm cryptsync leave` for cooperative removal and at the lost-envoy runbook for adversarial.
- A lost-envoy runbook in `docs/operations/` (or equivalent) is a separate follow-up: list the steps for re-initing a compromised fleet, what re-anchors (gitmirror project_ids), what gets orphaned (offline envoys can't follow the rotation), what other credentials need rotation. Out of scope for this ADR; tracked separately.

**Operational:**
- No version bump. No code change ships with this ADR.
- ADR-016 status note will be updated to mark the "Eviction is fleet-level" decision as superseded by ADR-017.

**Behavioral consequences:**
- For routine "I'm decommissioning this envoy" workflows, users run `vigo swarm cryptsync leave` on the departing envoy. Same mechanism that exists today.
- For "this envoy was compromised" workflows, the answer is "re-init the fleet and rotate everything else too." The pain of re-init is real and is the next thing to chip away at — see follow-up work on reducing re-init blast radius — but the pain of re-init is the honest cost of a compromised shared key, not a deficiency in the eviction surface.
- Future fleet primitives that change the threat model (e.g., per-envoy fleet-membership keys distributed via a CA-style fleet trust root, where revoking one envoy doesn't compromise the others) could re-open this decision. Such a primitive would be its own ADR.
