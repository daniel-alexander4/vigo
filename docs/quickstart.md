
# Quickstart

The guided path from an empty server to a converging, compliance-reporting fleet is the **[Tutorials](start/)** — four short pages, each building on the last:

1. [Stand up the server](start/stand-up-the-server.md) — run vigosrv, log in, enroll the server's own host.
2. [Enroll your first envoy](start/enroll-your-first-envoy.md) — token + remote install.
3. [Your first configcrate](start/your-first-configcrate.md) — write → bind → publish → converge → watch drift get corrected.
4. [Your first compliance report](start/your-first-compliance-report.md) — coverage → claim a control → export.

## The fast path (TL;DR)

If you've done this before, the whole arc is:

```bash
make up                                             # start the server (:8443 UI, :1530 gRPC)
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh   # enroll the server's own host (loopback)
vigocli tokens generate --pattern '*.example.com'   # mint a token for remote envoys
# on each remote host:
curl -sSfk https://<server>:8443/bootstrap | sudo sh -s -- --token <token>

# write stockpile/configcrates/motd.vgo (configcrate: motd ...), bind it, then:
vigocli config publish
vigocli envoys push --all                           # don't wait for the next interval
vigocli runs list --limit 5                         # confirm it applied
```

The [Tutorials](start/) walk each of these with what you'll see at every step.

---
*Confidential — Alexander4, LLC. Not for redistribution. See [legal/license.md](legal/license.md).*
