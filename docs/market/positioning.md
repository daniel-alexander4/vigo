---
name: Vigo Market Analysis & Positioning Materials
description: Positioning copy, battle cards, and messaging framework for Vigo across target markets — ready for website, sales decks, and outreach
type: project
---

# Vigo Market Analysis & Positioning Materials

Created 2026-03-19. Companion to [market_15s_convergence.md](market_15s_convergence.md) and [strategy_msp_compliance.md](strategy_msp_compliance.md).

---

## Core Positioning Statement

**For** operations teams and compliance officers at regulated organizations,
**who** need continuous proof that their infrastructure matches policy,
**Vigo is** a state enforcement engine
**that** detects configuration drift in 15 seconds, remediates automatically, and produces audit-ready evidence continuously.
**Unlike** Ansible (push-only, no drift detection), Puppet/Chef (heavy Ruby agents, complex DSLs), or RMM tools (no desired-state enforcement),
**Vigo** combines a 5 MB Rust agent, plain YAML configs, and sub-second remediation push into a single system that both enforces and proves compliance.

---

## The Three Claims

Every piece of marketing should reinforce one or more of these:

1. **15-second convergence** — Drift detected and corrected before your next sip of coffee. Not 30 minutes. Not 5 minutes. 15 seconds.

2. **Audit-ready by default** — Every check-in produces a compliance record. Every remediation is logged. Export to SIEM, CMDB, or OSCAL with one click. Your auditor gets a report, not a spreadsheet you built at 2 AM.

3. **Zero-complexity operations** — 5 MB agent. YAML configs. No Ruby, no DSL, no 200 MB runtime. A sysadmin unfamiliar with Vigo can read a config and understand it immediately.

---

## Messaging by Audience

### For the CTO / VP Engineering

> Your team is spending 40% of their time proving compliance instead of building product. Vigo makes compliance a byproduct of operations, not a separate workstream. Install the agent, write your policy in YAML, and every node continuously enforces and reports its state. When the auditor calls, export the report and get back to work.

### For the CISO / Compliance Officer

> PCI DSS 4.0 requires continuous monitoring with automated detection of unauthorized changes. SOC 2 Type II auditors want evidence of ongoing control effectiveness. HIPAA requires technical safeguards with audit controls. Vigo produces this evidence automatically, every 15 seconds, for every node. No manual evidence collection. No quarterly scrambles. Continuous compliance, continuously proven.

### For the DevOps / Platform Engineer

> It's Ansible's simplicity with Puppet's enforcement, minus the Ruby. Write YAML. Push config. Every node converges in 15 seconds. The agent is 5 MB of Rust — it runs on Linux, macOS, FreeBSD, Windows, even network devices. `depends_on` builds a DAG. `notify` triggers cascades. `when:` conditions filter by OS traits. You already know how this works.

### For the MSP Owner

> Your clients expect you to keep their systems secure and compliant. Your RMM tells you what's installed, not what should be. Vigo fills the gap: define the desired state, enforce it continuously, and hand your client an audit report that proves it. 25 nodes free. No per-tech licensing. One dashboard for every client.

---

## Battle Cards

### Vigo vs Ansible

| | Ansible | Vigo |
|---|---|---|
| Model | Push (SSH) | Pull (agent) + push (stream) |
| Drift detection | None — fire and forget | Continuous, every 15 seconds |
| Agent | Agentless (SSH + Python) | 5 MB Rust binary |
| Config language | YAML (playbooks) | YAML (.vgo files) |
| Idempotency | Module-dependent | Enforced by design |
| Compliance evidence | Manual (AWX/Tower reports) | Built-in, continuous, exportable |
| Offline operation | None | Cached policy, converge without server |
| Scale ceiling | ~500 nodes (SSH fan-out) | 100K+ nodes (gRPC streaming) |

**When they say:** "We already use Ansible."
**You say:** "Ansible is great for provisioning. But who's watching the servers after the playbook finishes? Vigo runs continuously. If someone edits a config, installs an unauthorized package, or disables a service, Vigo detects it in 15 seconds and puts it back. Ansible can't do that."

### Vigo vs Puppet

| | Puppet | Vigo |
|---|---|---|
| Agent size | ~200 MB (Ruby runtime) | 5 MB (Rust, static binary) |
| Config language | Puppet DSL (Ruby-based) | YAML |
| Convergence interval | 30 minutes | 15 seconds |
| Learning curve | Weeks (classes, modules, Hiera, r10k) | Hours (YAML, roles, modules) |
| Compliance reporting | Puppet Comply ($$$ add-on) | Built-in |
| Streaming push | No | Yes (gRPC bidirectional) |
| Cost (1000 nodes) | ~$100K/year (PE) | ~$132K/year (Growth tier) |

**When they say:** "We're evaluating Puppet Enterprise."
**You say:** "Puppet is powerful but complex. Your team will spend weeks learning the DSL, setting up r10k, configuring Hiera. With Vigo, your sysadmins write YAML they can read on day one. And Puppet's 30-minute convergence window means drift sits for half an hour. Vigo catches it in 15 seconds."

### Vigo vs Chef

| | Chef | Vigo |
|---|---|---|
| Agent size | ~200 MB (Ruby runtime) | 5 MB (Rust, static binary) |
| Config language | Ruby DSL (recipes/cookbooks) | YAML |
| Convergence interval | 30 minutes | 15 seconds |
| Vendor status | Progress Software (legacy) | Active development |
| Open source | Cinc (fork) after license change | Agent: Elastic-2.0 |
| Compliance | Chef InSpec (separate tool) | Built-in |

**When they say:** "We use Chef InSpec for compliance."
**You say:** "InSpec detects drift but doesn't fix it. You need Chef Client for remediation and InSpec for reporting — two tools, two languages, two maintenance burdens. Vigo does both in one system. And InSpec scans run periodically. Vigo enforces continuously."

### Vigo vs Salt

| | Salt | Vigo |
|---|---|---|
| Agent size | ~100 MB (Python) | 5 MB (Rust) |
| Transport | ZeroMQ / SSH | gRPC over mTLS |
| Real-time events | Yes (event bus) | Yes (AgentStream) |
| Full convergence | Highstate: 5-30 min | 15 seconds |
| Config language | YAML + Jinja2 | YAML + Go templates |
| Compliance | Salt Comply (VMware) | Built-in |
| Vendor status | VMware (uncertain roadmap) | Active development |

**When they say:** "Salt has real-time events too."
**You say:** "Salt's event bus is fast for ad-hoc commands, but highstate — full convergence — still takes minutes. And since Broadcom acquired VMware, Salt's roadmap is uncertain. Vigo gives you both: sub-second push for urgent changes and 15-second full convergence as a safety net."

### Vigo vs RMM Tools (ConnectWise, Datto, NinjaRMM)

| | RMM Tools | Vigo |
|---|---|---|
| Purpose | Remote access + patching | Desired-state enforcement |
| Drift detection | No | Yes, every 15 seconds |
| Remediation | Manual (scripts, remote sessions) | Automatic |
| Compliance reporting | Basic (patch status) | Full (config state, drift history, OSCAL export) |
| Config management | Ad-hoc scripts | Declarative YAML policy |

**When they say:** "Our RMM handles everything."
**You say:** "Your RMM tells you what's installed. It doesn't tell you what should be installed, and it can't enforce it continuously. When your client's auditor asks 'prove this firewall rule has been in place for 90 days,' your RMM can't answer. Vigo can — with timestamped evidence for every check-in."

---

## Industry-Specific Positioning

### Payment Processing (PCI DSS 4.0)

**Headline:** "PCI DSS 4.0 Continuous Monitoring in 15 Seconds"

**Body:** PCI DSS 4.0 Requirement 10.7.2 mandates automated detection of unauthorized changes to critical system configurations. Requirement 11.5 requires file integrity monitoring. Vigo satisfies both: every 15 seconds, every node's configuration is verified against policy, deviations are logged and auto-remediated, and the full audit trail is exportable to your QSA. No manual evidence collection. No quarterly scrambles before your ROC assessment.

**Proof points needed:**
- PCI DSS 4.0 requirement mapping document
- Sample QSA report showing Vigo evidence
- Compliance export demo (OSCAL format)

### Online Gambling

**Headline:** "Provable System Integrity for Gaming Regulators"

**Body:** Your gaming commission can audit at any moment. When they ask "prove this RNG server's configuration hasn't been tampered with," you need an answer in seconds, not days. Vigo continuously enforces your approved configuration across every system in your gaming infrastructure. Every check-in produces a signed, timestamped compliance record. Configuration drift is detected in 15 seconds and automatically remediated. Export the full audit trail in one click.

**Proof points needed:**
- Gaming commission requirement mapping (Nevada, Malta, UKGC)
- Demo with simulated gaming infrastructure
- Signed audit trail export

### Healthcare (HIPAA)

**Headline:** "HIPAA Technical Safeguards on Autopilot"

**Body:** HIPAA's Security Rule requires technical safeguards including access controls, audit controls, and integrity controls for systems handling ePHI. Vigo enforces your security baseline continuously: SSH hardening, firewall rules, service configurations, file permissions — all verified every 15 seconds. When a system drifts, Vigo remediates and logs the event. Your compliance team gets continuous evidence without bothering your operations team.

**Proof points needed:**
- HIPAA Security Rule mapping document
- Pre-built HIPAA hardening modules (.vgo)
- Integration with healthcare SIEM (Medigate, Claroty)

### MSSPs

**Headline:** "Turn Compliance Into a Managed Service"

**Body:** Your clients hire you to keep them secure. But proving it — that's where the margin disappears. Vigo lets you define security baselines per client, enforce them continuously across every endpoint, and generate audit-ready reports without lifting a finger. 25 nodes free per client. Scale to thousands. One dashboard, every client, 15-second convergence. When your client's auditor calls, export the report and bill for the hour.

**Proof points needed:**
- Multi-tenant demo (spanner hub/spoke)
- MSP pricing calculator
- Sample client-facing compliance report
- ConnectWise/Autotask webhook integration

### Cryptocurrency Exchanges

**Headline:** "Infrastructure Integrity for Digital Asset Custody"

**Body:** Your hot wallet servers, signing infrastructure, and trading engines are the highest-value targets on the internet. A misconfigured firewall rule or unauthorized SSH key is the difference between a normal Tuesday and a nine-figure loss. Vigo enforces your security policy on every system, every 15 seconds. mTLS transport, ED25519 signature verification, and automatic remediation. If something changes that shouldn't, Vigo puts it back before the next block confirms.

**Proof points needed:**
- Security architecture whitepaper (mTLS, signatures, secret handling)
- Pre-built crypto infrastructure modules (HSM config, key management, network segmentation)
- SOC 2 Type II evidence export demo

---

## Objection Handling

**"We don't need 15-second check-ins."**
You're right — most nodes don't. That's why Vigo defaults to 5 minutes and lets you configure per-node. But your PCI cardholder environment, your trading servers, your patient data systems? Those nodes get 15-second convergence. The rest stay at 5 minutes. You choose where to spend the cycles.

**"How does a 5 MB agent handle all this?"**
Rust. No garbage collector, no runtime, no interpreter. The agent is a single static binary that runs on everything from a Raspberry Pi to a 256-core server. It checks state, applies changes if needed, and sleeps. Most cycles take less than 200ms.

**"We already have Terraform for infrastructure."**
Terraform provisions infrastructure. Vigo manages what's on it after provisioning. Terraform tells AWS to create a VM. Vigo ensures that VM stays configured correctly for the next 3 years. They're complementary, not competing.

**"What happens when the server goes down?"**
The agent caches its last-known policy in a local LMDB store and continues converging autonomously. Results queue up and deliver when connectivity returns. Your nodes don't stop being compliant just because the server rebooted.

**"We can't install agents on our systems."**
The agent is a single binary with no dependencies — no Python, no Ruby, no Java. It runs as a systemd service, uses mTLS for all communication, and its configuration is read-only from the server. It touches only what your policy defines. Many compliance frameworks actually require an agent for continuous monitoring.

**"Your pricing is higher than Puppet."**
Puppet Enterprise at 1,000 nodes is ~$100K/year. Vigo Growth tier is $132K. But Puppet requires Puppet Comply ($$$) for compliance reporting, Hiera + r10k for config management, and weeks of team training. Vigo includes compliance reporting, needs no add-ons, and your team writes YAML on day one. Total cost of ownership is lower.

---

## Website Copy Suggestions

### Homepage Hero

> **Configuration drift ends here.**
>
> Vigo detects drift in 15 seconds. Remediates automatically. Proves compliance continuously. One 5 MB agent. Plain YAML. Every node, every check-in, audit-ready.
>
> [Get Started Free — 25 Nodes] [Watch Demo]

### Features Page Tagline

> **Enforce. Detect. Prove. Repeat.**
>
> Every 15 seconds, every node checks in. If it matches policy, Vigo records the proof. If it doesn't, Vigo fixes it and records that too. Your auditor gets a report. Your team gets their weekend back.

### Compliance Page Tagline

> **Compliance that runs itself.**
>
> Map your controls to Vigo modules. Enforce them continuously. Export evidence in OSCAL, SIEM, or CMDB format. PCI DSS 4.0, HIPAA, SOC 2, NERC CIP, CMMC — one tool, one policy, one audit trail.
