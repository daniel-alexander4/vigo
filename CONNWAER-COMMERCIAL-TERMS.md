# Vigo Connwaer Commercial Terms

Effective 2026-03-22. Last updated 2026-05-20. Alexander4, LLC reserves the right to update these
terms. Changes apply to new license terms and renewals only. Existing
agreements, including multi-year commitments, are governed by the terms
in effect at the time of signing for the duration of that agreement.

## Product Overview

Vigo Connwaer is a standalone compliance verification agent. It runs
alongside the Vigo agent on managed nodes and performs active
verification tasks: backup restore testing, WORM storage verification,
application allowlisting, RNG statistical validation, kill switch
testing, HSM key lifecycle auditing, capacity benchmarking, log review,
and BCP/DR testing.

## Why Connwaer Is a Separate Product

Vigo is a configuration management engine. It declares desired state,
enforces it idempotently, and reports whether the system matches. That
is a well-defined scope, and everything Vigo does falls within it.

Connwaer does something fundamentally different. It does not declare or
enforce state — it conducts active experiments against live systems to
answer questions that configuration alone cannot. Restoring a backup to
prove it works is not configuration management. Running a statistical
test suite against a random number generator is not configuration
management. Attempting a write to a WORM volume to confirm immutability
is not configuration management. These are empirical verification tasks:
they execute real operations, measure real outcomes, and produce evidence
that a control actually functions — not just that it is configured.

Bundling Connwaer into Vigo would blur that boundary. Vigo's promise is
simple: every feature is included in every tier, and a license unlocks a
node count. Adding a specialized verification engine to that promise
would force every customer to pay for capabilities most do not need,
while making the product harder to reason about. Keeping them separate
means each product does one thing, prices reflect actual value delivered,
and operators who only need configuration management are not subsidizing
compliance verification they never use.

Connwaer requires Vigo — it depends on the Vigo agent's trait collection
pipeline to report results to the server. An active Vigo license (any
tier, including Community) is required.

## Licensing

Vigo Connwaer is proprietary software. Copyright (c) 2026 Alexander4, LLC.
All rights reserved.

You may use Connwaer on up to 25 nodes at no cost, provided those nodes
are managed by a valid Vigo installation. Beyond 25 nodes, a commercial
Connwaer license is required.

All capabilities are included in every tier. There are no gated
capabilities, premium modules, or capability packs. A license unlocks a
node count — every node gets every capability.

License files are validated locally and do not require network
connectivity, internet access, or activation servers. Connwaer is fully
operational in air-gapped and isolated network environments.

## Ownership and Reservation of Rights

All right, title, and interest in and to Connwaer — including all
intellectual property rights, source code, object code, designs,
algorithms, data structures, protocols, trademarks, trade names,
service marks, logos, documentation, and any modifications,
enhancements, improvements, or derivative works thereof, whether
created by Alexander4, LLC or contributed by any other person — are and
shall remain the exclusive property of Alexander4, LLC.

A Connwaer license grants only a limited right to use the software and
conveys no ownership, title, or other proprietary interest in it. Use
of Connwaer, in any capacity and at any licensing tier (including the
Community tier), creates no right, title, or interest in the software
in favor of any user or organization. You acknowledge that you acquire
no such interest by using Connwaer, and you waive and disclaim any
claim of ownership, co-ownership, joint authorship, or other
proprietary right in or to Connwaer, its trademarks and names, or any
modification or derivative work of it.

Nothing in these terms shall be construed as a transfer or assignment
of any intellectual property right. All rights not expressly granted
are reserved by Alexander4, LLC.

## Pricing

All amounts are in United States Dollars (USD). Pricing is per managed
node running Connwaer, per year. Your node count determines your tier,
and that tier's per-node rate applies to all your nodes.

| Tier        | Nodes            | Per Node / Year | Support Level |
|-------------|------------------|-----------------|---------------|
| Community   | 1 – 25           | Free            | Community     |
| Starter     | 26 – 250         | $96             | Standard      |
| Growth      | 251 – 1,000      | $84             | Standard      |
| Business    | 1,001 – 5,000    | $72             | Priority      |
| Enterprise  | 5,001 – 25,000   | $60             | Dedicated     |
| Scale       | 25,001 – 100,000 | $48             | Dedicated     |

Example: a 500-node deployment pays 500 × $84 = **$42,000/year**.

Connwaer node counts are independent of Vigo node counts. You are not
required to run Connwaer on every Vigo-managed node. License only the
nodes that need active compliance verification.

Billed annually. Payment is due within thirty (30) days of invoice
date (Net 30). Custom payment terms may be negotiated as part of an
enterprise agreement.

**Scale-down:** You may request a reduced node count at any time. An
updated license file will be issued for the lower count. No refund is
issued for the current term — the reduced rate takes effect at the next
billing cycle. Multi-year agreements with a node floor cannot be
reduced below the committed floor.

Refund policy: if you are unsatisfied within the first
thirty (30) days of a new license term, you may request a full refund.
After thirty days, licenses are non-refundable for the remainder of the
term, except where required by applicable law (including EU consumer
protection regulations) or as described in the Right to Refuse section
below.

## What Connwaer Verifies

Connwaer includes 18 verification capabilities:

| Capability | What It Does | Frameworks |
|---|---|---|
| Application allowlisting | Verifies fapolicyd/AppLocker enforcing, rules match policy, unauthorized exec blocked | CIS Controls (CE-MW-4) |
| Sandboxing | Verifies AppArmor/SELinux/WDAC enforcing, profiles loaded, Credential Guard active | CIS Controls (CE-MW-5) |
| Log review | Automated auditd/Event Log analysis against configurable rules, anomaly detection | PCI DSS (10.4.1) |
| Capacity monitoring | Synthetic benchmarks (disk I/O, network, CPU, memory), trend analysis, projected exhaustion | MiFID II (RTS7-Art11) |
| Backup restore testing | Executes restore, verifies checksums, measures actual RTO against target | FINRA (BCP-1, BCP-2) |
| BCP/DR testing | Orchestrated restore + recovery sequence, composite report, framework-specific output | FINRA (BCP-3), Gaming (MGA-RGR-5.2, UKGC-RTS-5B) |
| WORM verification | Attempts test write to WORM volume (must fail), verifies timestamps, checks mount/lock config | FINRA (SEC-17a4-1, SEC-17a4-2) |
| Kill switch validation | Verifies binary present, config valid, dry-run succeeds, targets reachable | MiFID II (RTS7-Art14) |
| RNG validation | NIST SP 800-22 statistical test suite (15 tests), configurable sample size, per-test p-values | Gaming (NGC-TS-4.2, MGA-RGR-6.1) |
| HSM key lifecycle | PKCS#11 key enumeration, expired key destruction verification, attribute auditing | Cryptocurrency (CCSS-9.1) |
| Encryption at rest | Verifies LUKS/BitLocker/FileVault active on all data volumes | HIPAA (164.312(a)(2)(iv)), PCI DSS (3.4) |
| Password policy | Verifies PAM/GPO password complexity, history, lockout against configured thresholds | NIST 800-53 (IA-5), SOC 2 (CC6.1) |
| Network segmentation | Probes cross-segment connectivity, verifies firewall rules block unauthorized traffic | PCI DSS (1.3), NERC CIP (CIP-005) |
| Privilege escalation | Verifies sudo/SUID configuration, checks for unauthorized privilege paths | NIST 800-53 (AC-6), SOX (ITGC-AC-2) |
| Time synchronization | Verifies NTP/chrony sync status, stratum, and offset against configured thresholds | PCI DSS (10.4), SOC 2 (CC7.2) |
| Integrity monitoring | Verifies AIDE/OSSEC/Tripwire database freshness and detects unauthorized file changes | HIPAA (164.312(c)(2)), NIST 800-53 (SI-7) |
| Session timeout | Verifies SSH/RDP/console idle timeout configuration matches policy | HIPAA (164.312(a)(2)(iii)), CIS Benchmarks |
| SNMP collector | Collects device health data via SNMP for network equipment monitoring | ISO 27001 (A.13.1), NERC CIP (CIP-007) |

Every capability runs on a configurable schedule, writes structured
evidence to disk, and reports results through the Vigo trait pipeline.
Results appear in compliance reports, envoy detail views, live queries,
and templates — no additional server configuration required.

## Combined Pricing Examples

Connwaer is licensed separately from Vigo. Both licenses are required.
Vigo uses graduated brackets (first 100 nodes free, then per-bracket
rates summed); Connwaer uses flat-tier pricing per the table above.

| Customer Profile | Nodes | Vigo License (graduated) | Connwaer License (flat-tier) | Combined Annual |
|---|---|---|---|---|
| Gaming operator | 100 | Free (Community) | $9,600 (Starter) | $9,600 |
| MSP (compliance) | 250 | $21,600 (Starter) | $24,000 (Starter) | $45,600 |
| Regional bank | 500 | $54,600 (Growth) | $42,000 (Growth) | $96,600 |
| Brokerage (FINRA) | 2,000 | $240,600 (Business) | $144,000 (Business) | $384,600 |
| Crypto exchange | 200 | $14,400 (Starter) | $19,200 (Starter) | $33,600 |

## Loyalty Discount

Long-term customers pay less every year. Starting in year 3, your rate
decreases by 3% per year from 100% down to a 75% floor in year 12:

| Year   | You Pay | 1K-node Growth customer pays |
|--------|---------|------------------------------|
| 1 – 2  | 100%   | $84,000                      |
| 3      | 97%    | $81,480                      |
| 4      | 94%    | $78,960                      |
| 5      | 91%    | $76,440                      |
| 6      | 88%    | $73,920                      |
| 7      | 85%    | $71,400                      |
| 8      | 82%    | $68,880                      |
| 9      | 79%    | $66,360                      |
| 10     | 76%    | $63,840                      |
| 11     | 75%    | $63,000                      |
| 12+    | 75%    | $63,000                      |

The discount applies to the per-node rate at every tier. The loyalty
floor is 75% — your loyalty rate will never drop below three-quarters
of the list price. Multi-year rates lock independently of the loyalty
curve and may go further during the term (see Multi-Year Commitments).
The loyalty clock starts from the date of your first paid Connwaer
license and is tied to your organization, not a specific license term.
Continuous renewal is required; a lapse of more than 90 days resets the
loyalty clock.

The Connwaer loyalty clock is independent of the Vigo loyalty clock.
Each product tracks its own tenure.

## Multi-Year Commitments

Customers may commit to a multi-year term in exchange for a discounted
per-node rate locked for the duration of the agreement.

| Term    | Discount | Example: 2,000 nodes at Business ($72) |
|---------|----------|----------------------------------------|
| 1 year  | 0%       | $144,000/yr                            |
| 3 years | 15%      | $122,400/yr ($367,200 total)           |
| 5 years | 22%      | $112,320/yr ($561,600 total)           |
| 7 years | 27%      | $105,120/yr ($735,840 total)           |

**Payment:** Billed annually at the start of each contract year at the
locked discounted rate.

**Rate lock:** The per-node rate is fixed for the full term, regardless
of any list price changes during the commitment period.

**Node floor:** Multi-year agreements include a minimum node commitment.
You are billed for the committed node count even if actual usage is
lower. Nodes above the floor are billed at the same locked rate.

**Early termination:** You may terminate a multi-year agreement before
the end of the term. Upon early termination, you owe the difference
between the discounted rate you paid and the undiscounted list rate in
effect at the time the multi-year agreement was signed, for all years
already completed. No refund is issued for the current year. Loyalty
credit granted at signing is rolled back proportionally to the unserved
years — a 5-year deal terminated after year 2 retains 2 years of credit;
the remaining 3 years are removed.

**Loyalty discount interaction:** The multi-year discount and the
loyalty discount are not stacked in the same year — during the term,
the locked multi-year rate applies. Signing a multi-year agreement
advances your Connwaer loyalty clock by the length of the term: a 3-year
deal grants 3 years of loyalty credit at signing, a 5-year deal grants
5 years, a 7-year deal grants 7 years. Credit is applied at signing,
accumulates across consecutive multi-year commitments, and is tied to
your organization. Committed customers therefore reach the 75% loyalty
floor years earlier than annual-renewing customers.

**Combining with Vigo multi-year:** Connwaer and Vigo multi-year
commitments are independent agreements. You may commit to different
terms for each product. A combined enterprise agreement covering both
products under a single contract is available — contact us for details.

## Enterprise Agreements

Enterprise agreements are available for organizations running Connwaer
across multiple sites, networks, or spanner instances under a single
contract.

**Aggregate node counting:** Your tier and per-node rate are determined
by the total number of nodes running Connwaer across all sites. A single
Connwaer license file is issued with the aggregate node count.

**Node floor:** Enterprise agreements include a minimum node commitment
across all sites. You are billed for the committed floor even if actual
aggregate usage is lower.

**Combining with multi-year:** Enterprise agreements may be combined
with multi-year commitments for both aggregate pricing and term
discounts.

**Combined Vigo + Connwaer enterprise agreements:** Organizations
licensing both Vigo and Connwaer at enterprise scale may negotiate a
single combined agreement. Contact Alexander4, LLC for unified pricing
and terms.

**Organizational changes:** Changes to organizational structure,
including acquisitions, mergers, or divestitures, require a license
amendment. Contact Alexander4, LLC to adjust your node count and issue
an updated license file.

To discuss an enterprise agreement, contact connwaer.licensing@alexander4.io.

## Vigo Prerequisite

Connwaer requires an active Vigo installation on every node where
Connwaer runs. The Vigo agent collects Connwaer's results via its trait
pipeline and reports them to the Vigo server for compliance dashboards,
queries, and reports.

If the Vigo agent is not installed or not running on a node, Connwaer
will still execute its verification capabilities and write results to
disk, but those results will not be visible in the Vigo server UI or
available for fleet-wide queries.

The Vigo license and Connwaer license are separate. Your Vigo node count
does not need to match your Connwaer node count. You may run Connwaer on
a subset of your Vigo-managed fleet.

## Support

Connwaer support is provided under the same support level as your
Connwaer license tier:

| Level       | Included With    | Response Time    | Channel                          |
|-------------|------------------|------------------|----------------------------------|
| Community   | Community tier   | Best effort      | Email                            |
| Standard    | Starter, Growth  | 2 business days  | Email                            |
| Priority    | Business+        | 1 business day   | Email + direct                   |
| Dedicated   | Enterprise+      | 4 business hours | Email + direct + scheduled calls |

Response times are service targets and not guaranteed SLAs. Guaranteed
SLAs with service credits are available as part of custom enterprise
agreements.

Support covers capability configuration, result interpretation, bug
triage, and upgrade assistance. It does not cover writing your
verification scripts, interpreting regulatory requirements, or providing
compliance advice.

If you hold both Vigo and Connwaer support entitlements at different
levels, the higher support level applies to issues that span both
products.

## Feature Requests

All paid customers may submit feature requests via email. Requests are
reviewed and prioritized at our discretion — higher tiers receive priority
consideration, but no tier guarantees delivery of a specific feature or
timeline.

Free-tier users are welcome to suggest features via email.

For guaranteed delivery of a specific capability or integration, custom
development is available as a separate engagement, scoped and quoted
individually. Contact us for details.

## Use Prohibitions

This software may not be used by, for, or on behalf of:

- Any military organization, department of defense, armed forces,
  defense contractor, intelligence agency, or any entity primarily
  engaged in military or weapons-related activities. This includes but
  is not limited to the United States Department of Defense, its
  branches, agencies, and contractors operating under defense contracts.
- Any manufacturer, distributor, or retailer of firearms, ammunition,
  weapons, or weapons components.
- Any manufacturer, distributor, or retailer of pesticides, herbicides,
  or chemical poisons intended to kill living organisms.
- Any operator of private prisons, for-profit detention facilities, or
  for-profit immigrant detention centers.
- Any developer, vendor, or operator of surveillance technology designed
  for mass monitoring, including spyware, location tracking, or
  communications interception targeting individuals without their consent.

No license — free or paid — will be granted for such use. These
prohibitions are absolute and non-negotiable.

## Right to Refuse

Alexander4, LLC reserves the right to decline or terminate any commercial
license for any reason, at its sole discretion, including but not limited to:

- Use cases that conflict with the values or reputation of the project
- Organizations that have previously violated the license terms
- Requests that we determine we cannot adequately support

If a license is declined, no payment is collected.

If a license is terminated by Alexander4, LLC due to a violation of
the license terms (including Use Prohibitions), no refund is issued
and any applicable multi-year early termination clawback applies.

If a license is terminated by Alexander4, LLC for reasons unrelated
to a violation (values, reputation, inability to support), a pro-rata
refund is issued for the remaining term and no early termination
clawback applies.

## Grace Period

If your active Connwaer node count exceeds your licensed limit, Connwaer
enters a 15-day grace period. During grace, Connwaer operates normally
on all nodes but logs a warning on every verification run. After 15
days, Connwaer stops executing capabilities on unlicensed nodes. Licensed
nodes continue operating normally.

Reduce your node count or upgrade your Connwaer license to return to
compliant status at any time — the grace clock resets immediately.

**Late payment:** If payment is not received within thirty (30) days
of the due date, Alexander4, LLC will provide written notice of the
overdue balance. If the balance is not paid within fifteen (15) days
of notice, the Connwaer license is suspended — capabilities stop
executing on all nodes. If payment remains outstanding for sixty (60)
days past the due date, the license terminates.

**Continuity:** If the Connwaer license expires or is suspended, the
Connwaer binary stops executing new verification runs. The last-written
result files remain on disk and continue to be reported by the Vigo
agent's trait collector until they are cleared. No verification data
is deleted by license enforcement.

## Data Processing

Vigo Connwaer is self-hosted software. Alexander4, LLC does not collect,
process, store, or transmit any of your data, your managed nodes' data,
or your verification results. The Software runs entirely within your
infrastructure.

**License telemetry:** The license enforcement system validates node
counts locally. No usage data, node identifiers, hostnames, IP
addresses, verification results, or compliance evidence are transmitted
to Alexander4, LLC or any third party.

**Support interactions:** If you contact support and share logs,
configuration excerpts, or verification results, that information is
used solely for resolving your issue and is not retained beyond the
support case.

If you are subject to GDPR, CCPA, or similar data protection
regulations, your use of Connwaer does not require a Data Processing
Agreement (DPA) with Alexander4, LLC because no personal data flows to
us. You remain the sole data controller for any data processed by the
Software within your environment.

## No Warranty and Limitation of Liability

Connwaer is provided "AS IS", without warranty of any kind. The full
No Warranty, Limitation of Liability, Severability, and Entire Agreement
provisions set forth in the Vigo LICENSE file apply to Connwaer in their
entirety. Alexander4, LLC's total aggregate liability for Connwaer is
capped at the amounts paid for Connwaer (not Vigo) during the twelve
months preceding the claim. For Community tier users, the cap is fifty
U.S. dollars ($50).

## Governing Law

These commercial terms are governed by the same Governing Law, Dispute
Resolution, and Severability provisions set forth in the Vigo LICENSE
file.

## Contact

Email: connwaer.licensing@alexander4.io

All commercial inquiries and custom terms should be directed to this
address.
