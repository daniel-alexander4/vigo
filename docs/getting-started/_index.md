
# Getting Started

This section covers what Vigo is, how to install it, and how to get your first envoy enrolled and configured.

## Overview

- [What is Vigo](overview.md) — Architecture, design principles, how it works

## Installation & Setup

- [Installation](installation.md) — Install the server, CLI, and agent
- [Quickstart](quickstart.md) — 10-minute first-envoy walkthrough
- [Bootstrap](bootstrap.md) — How agent enrollment works
- [First Module](first-module.md) — Write your first .vgo module
- [Server Configuration](server-config.md) — Full server.yaml reference

## Performance & Comparison

- [Technical Comparison](technical-comparison.md) — Vigo vs Puppet, CFEngine, Chef, Ansible, SaltStack — architecture, security, performance, scale (all theoretical for Vigo — no production deployments)
- [Performance: 1-minute intervals](performance-1m.md) — per-request cost derivations + capacity projections
- [Performance: 30-second intervals](performance-30s.md) — recommended setting for responsiveness-sensitive fleets
- [Performance: 15-second intervals](performance-15s.md) — near-real-time, the practical sweet spot
- [Performance: 5-second intervals (1,000 nodes)](performance-5s.md) — the edge of polling; every CM compared; calculated, not measured

For deeper engineering references (architecture, module-writing language, subsystem guides, custom plugin authoring), see [Engineering](../engineering/_index.md).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
