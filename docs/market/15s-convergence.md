
---
name: 15-Second Convergence Market Analysis
description: Industry analysis of which markets would adopt Vigo for its 15-second check-in convergence — tiered by urgency and willingness to pay
type: project
---

# 15-Second Convergence: Target Industries

Created 2026-03-19. Based on Vigo's architecture analysis showing 0.5ms server overhead per no-change check-in, zero-DB hot path, and streaming push capability.

## Vigo's Competitive Position

No competitor can match this combination:

| Tool | Per-cycle server cost | Per-cycle agent cost | Can do 15s? |
|---|---|---|---|
| **Puppet** | Catalog compile: 500ms-5s | Ruby eval: 10-60s | No |
| **Chef** | Cookbook resolve: 200ms-2s | Ruby eval: 10-60s | No |
| **Salt** | Event bus: ~1ms | Python highstate: 5-30s | Events yes, convergence no |
| **CFEngine** | None (autonomous) | C eval: 50-200ms | Yes, but no central visibility |
| **Vigo** | 0.5ms (no-change) / 25ms (rebuild) | Rust eval: 50-200ms | **Yes** |

**One-liner:** "15-second drift detection. Zero-second remediation push. 0.5ms server overhead per node."

---

## Tier 1: "Take my money" -- 15 seconds is a regulatory or operational requirement

### Payment Processing & Card Networks
- PCI DSS 4.0 (effective March 2025) explicitly requires "continuous monitoring" and automated detection of unauthorized changes
- Visa, Mastercard, payment gateways, acquiring banks
- A 5-minute drift window on a cardholder data environment is a finding. 15 seconds isn't.
- These companies already spend $500K+/year on compliance tooling
- **Node counts:** 500-50,000

### Online Gambling & Casino Operations
- Gaming commissions (Nevada, Malta, UK Gambling Commission) require provable system integrity in real-time
- Slot machines, sports betting platforms, random number generators -- regulators can audit at any moment
- Configuration drift on a gaming system = license revocation
- **This is a market nobody is serving well** -- they currently use custom scripts + manual audits
- **Node counts:** 200-5,000

### High-Frequency Trading / Market Makers
- SEC Rule 15c3-5 requires pre-trade risk controls
- A misconfigured firewall rule or limit change at 10:00:00 that isn't caught until 10:05:00 = millions in exposure
- These firms pay $50-100/node/month without blinking
- **Node counts:** 100-2,000 but extremely high willingness to pay

---

## Tier 2: "We need this yesterday" -- 15 seconds solves an active pain

### Managed Security Service Providers (MSSPs)
- SOC-as-a-service providers who guarantee continuous monitoring to their clients
- 15-second convergence = "we can prove your systems are hardened right now, not 30 minutes ago"
- Differentiation against competitors still running Ansible on cron
- **Node counts:** 1,000-100,000 across clients

### Healthcare Systems (HIPAA + Patient Safety)
- HIPAA requires technical safeguards with audit controls
- Medical device networks, EHR systems, pharmacy dispensing
- A misconfigured DICOM server or HL7 gateway = patient data exposure
- FDA 21 CFR Part 11 for validated systems -- provable configuration state
- **Node counts:** 500-20,000

### Critical Infrastructure / Energy (NERC CIP)
- NERC CIP-010 requires configuration monitoring and unauthorized change detection on BES Cyber Systems
- Power grid control systems, natural gas pipelines, water treatment
- CIP violations: $1M/day per violation. Not a typo.
- Currently using ancient tools (Tripwire, manual audits). Desperate for modernization.
- **Node counts:** 200-5,000 but compliance penalties make any price look cheap

### Cryptocurrency Exchanges & Custodians
- Regulatory frameworks emerging fast (EU MiCA, US state-by-state)
- Hot wallet infrastructure, signing servers -- configuration drift = potential loss of funds
- Mt. Gox-level paranoia about infrastructure integrity
- Young companies, modern stacks, would adopt Rust agent happily
- **Node counts:** 50-500 but very high per-node value

---

## Tier 3: "Interesting, let's pilot" -- 15 seconds is a competitive advantage

### SaaS Providers Pursuing SOC 2 Type II
- SOC 2 auditors increasingly want evidence of continuous control effectiveness, not point-in-time
- "Our configuration enforcement has a 15-second convergence window" in a SOC 2 report is a differentiator for their sales team
- **Node counts:** 100-10,000

### Defense Contractors (CMMC)
- CMMC 2.0 Level 2+ requires continuous monitoring of CUI-handling systems
- (Note: Vigo license prohibits military/DoD directly, but defense contractors doing commercial work may not fall under that)
- **Node counts:** 500-5,000

### Insurance / Actuarial
- Cyber insurance underwriters are starting to require provable configuration management
- Companies getting cyber insurance could lower premiums with 15s convergence evidence
- The insurer itself might mandate Vigo for policyholders
- **Node counts:** varies wildly

---

## Market Comparison

| Market | Urgency | Willingness to Pay | Competition | Ease of Entry |
|---|---|---|---|---|
| Payment Processing | Very High | Very High | Crowded | Hard |
| Online Gambling | Very High | High | **Empty** | Medium |
| HFT / Trading | Very High | Extreme | Custom-built | Hard |
| MSSPs | High | Medium | Ansible/Puppet | **Easy** |
| NERC CIP / Energy | Very High | Very High | Tripwire (ancient) | Medium |
| Healthcare | High | Medium | Tanium, Qualys | Medium |
| Crypto Exchanges | High | High | **Empty** | **Easy** |
| SOC 2 SaaS | Medium | Medium | Chef/Puppet | Easy |

---

## Recommendation

**Low-hanging fruit:** Online gambling and crypto exchanges -- high urgency, high willingness to pay, almost no competition, modern engineering teams that would embrace a Rust agent + YAML configs. Small enough markets that Puppet/Chef/Ansible won't bother competing.

**Best volume play:** MSSPs (existing go-to-market plan). The 15-second story makes the pitch to their clients irresistible.

**The whale:** NERC CIP -- $1M/day violation penalties make any price rational, but 12-18 month sales cycle and certification requirements.
