---
title: Platform Compliance
---

# Platform Compliance

The **Platform Compliance** page (`/platform/compliance`, under Platform → Data in the nav) lists what the Vigo server and agent satisfy by construction — independent of any envoy's module assignment. Use this surface for vendor questionnaires, customer security reviews, and SOC 2 audit responses where the question is "what does Vigo claim?" rather than "what does my fleet claim?"

## Why this is separate from envoy compliance

Prior to 0.32.0, the framework reports under `/api/v1/report/<slug>` baked Vigo's own architecture into per-envoy scores — every envoy was credited for mTLS transport, RBAC, audit hash-chaining, etc., even when the operator had assigned zero modules. That conflated two audiences:

- **Fleet operators** want an honest read of what their managed nodes enforce. A freshly enrolled envoy with no modules should report `0%` covered, not `45%` covered "because the server has good architecture."
- **Customers and auditors of Vigo itself** want to know what guarantees the product gives them about the management plane.

The 0.32.0 split keeps each surface honest. Envoy reports score envoy state only — module-tag coverage, Connwaer trait escalation, waivers. Platform Compliance is a fixed read-only registry of product-level capabilities; it never enters envoy or fleet scoring.

## What is on the page

Each card on `/platform/compliance` is one [`AppControl`](../engineering/architecture.md) entry. Cards carry:

- **Title** — the capability (e.g., "All transport encrypted with mTLS").
- **Description** — one or two sentences explaining what Vigo does.
- **Evidence** — technical detail an auditor wants to cite (algorithm names, file paths, code references).
- **Relevant to** — the framework controls this capability addresses, listed by framework label and native control ID (`HIPAA 164.312(e)(1)`, `SOC 2 CC6.1`, `PCI 2.2.7`, `NIST 800-53 SC-8`, …).

A filter chip row above the cards narrows the view to controls relevant to a single framework.

## Source of truth

The registry lives in [`server/compliance/appcontrols.go`](https://github.com/example/vigo/blob/main/server/compliance/appcontrols.go) (path inside the repo). Adding a new product-level capability is a code change to that file — there is no DB write path, no admin UI, no YAML override. This is deliberate: the registry describes what the product itself does, and the only honest way to update it is to ship code that backs the claim.

## Permissions

Reading the page requires `compliance.read`, which is granted to all three roles (`admin`, `viewer`, `compliance`) by default. There are no mutating endpoints on this surface.

## What this page is *not*

- **Not a customer-facing posture page.** It documents what Vigo enforces; it does not measure what a customer's fleet does. For per-framework operator-facing reports, see `/compliance/<slug>`.
- **Not a SOC 2 report.** Vendor questionnaires can cite this page as evidence Vigo provides specific controls, but the page is not itself an attestation. SOC 2 reports come from Alexander4's auditor.
- **Not editable.** No upload, no override, no waiver mechanism. To change what is claimed, change the code that backs the claim.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
