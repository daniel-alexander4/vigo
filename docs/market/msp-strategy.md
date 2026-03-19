---
name: MSP & Compliance Go-to-Market Strategy
description: Strategy for getting Vigo into MSP and compliance-driven organizations — market analysis, gaps, pricing, phased plan
type: project
---

# MSP & Compliance Go-to-Market Strategy

Created 2026-03-18. Based on competitive analysis against Ansible, Puppet, Chef, Salt, CFEngine and assessment of current Vigo capabilities.

## The Core Value Proposition

MSPs and compliance-driven orgs share the same pain: **"prove that N machines are configured correctly, right now."**

- RMM tools (ConnectWise, Datto, NinjaRMM) handle remote access and patching but are terrible at desired-state enforcement and continuous compliance evidence.
- Ansible can configure machines but can't prove they stay configured (push-only, stateless).
- Puppet/Chef can enforce and report but require 100-200 MB Ruby agents and complex DSLs.
- Vigo does both — enforce and prove, continuously, with a 5 MB Rust agent and YAML configs.

**One-liner:** "Install a 5 MB agent. Write YAML. Get continuous compliance with drift detection, auto-remediation, and audit-ready reports."

## Current Pricing

| Tier | Nodes | Per Node/Year | Monthly equiv |
|---|---|---|---|
| Community | 1-25 | Free | Free |
| Starter | 26-250 | $144 | $12/mo |
| Growth | 251-1,000 | $132 | $11/mo |
| Business | 1,001-5,000 | $120 | $10/mo |
| Enterprise | 5,001-25,000 | $108 | $9/mo |
| Scale | 25,001-100,000 | $84 | $7/mo |

## Key Risk: The CFEngine Problem

CFEngine has been technically elegant for 30 years — 3 MB C agent, autonomous, proven at 100K+ nodes — yet has 520 GitHub stars. Technical superiority alone does not drive adoption. Ecosystem, community, and approachability do. Vigo mitigates with YAML configs, pre-built compliance modules, modern web UI, and focus on a specific buyer (compliance).

See pending.md for the phased execution plan.
