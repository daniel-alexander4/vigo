# Vigo Commercial Terms

Vigo is a distributed state enforcement engine for managing infrastructure
at scale. It replaces ad-hoc scripts and legacy configuration management
tools with a single system that enforces desired state, tracks compliance
against regulatory frameworks, and provides real-time fleet visibility —
all self-hosted, with no external dependencies.

Effective 2026-03-16. Alexander4, LLC reserves the right to update these
terms. Changes apply to new license terms and renewals only. Existing
agreements, including multi-year commitments, are governed by the terms
in effect at the time of signing for the duration of that agreement.

## Licensing

Vigo is proprietary software. Copyright (c) 2026 Alexander4, LLC.
All rights reserved. See LICENSE for the full license text.

You may use Vigo to manage up to 25 nodes at no cost. Beyond 25 nodes,
a commercial license is required.

All features are included in every tier. There are no enterprise-only
features, gated add-ons, or premium modules. A license unlocks a node
count — nothing else changes.

License files are validated locally and do not require network
connectivity, internet access, or activation servers. Vigo is fully
operational in air-gapped and isolated network environments.

Paid license files are bound to a machine fingerprint derived from the
server's machine ID, primary network interface, and TLS CA certificate.
A license issued for one server cannot be used on a different server.
If you migrate to new hardware or reinstall the OS, contact
Alexander4, LLC for a re-issued license file at no charge.

## Pricing

All amounts are in United States Dollars (USD). Pricing is per managed
node, per year. Your node count determines your tier, and that tier's
per-node rate applies to all your nodes.

| Tier        | Nodes            | Per Node / Year | Support Level |
|-------------|------------------|-----------------|---------------|
| Community   | 1 – 25           | Free            | Community     |
| Starter     | 26 – 250         | $144            | Standard      |
| Growth      | 251 – 1,000      | $132            | Standard      |
| Business    | 1,001 – 5,000    | $120            | Priority      |
| Enterprise  | 5,001 – 25,000   | $108            | Dedicated     |
| Scale       | 25,001 – 100,000 | $84             | Dedicated     |

Example: a 500-node fleet pays 500 × $132 = **$66,000/year**.

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

## Loyalty Discount

Long-term customers pay less every year. After year 5, your rate
decreases linearly from 100% to 75% over seven years:

| Year  | You Pay | 1K-node Growth customer pays |
|-------|---------|------------------------------|
| 1 – 5 | 100%   | $132,000                     |
| 6     | 96%    | $126,720                     |
| 7     | 93%    | $122,760                     |
| 8     | 89%    | $117,480                     |
| 9     | 86%    | $113,520                     |
| 10    | 82%    | $108,240                     |
| 11    | 79%    | $104,280                     |
| 12+   | 75%    | $99,000                      |

The discount applies to the per-node rate at every tier. The floor is
75% — your rate will never drop below three-quarters of the list price.
The loyalty clock starts from the date of your first paid license and is
tied to your organization, not a specific license term. Continuous
renewal is required; a lapse of more than 90 days resets the loyalty clock.

## Multi-Year Commitments

Customers may commit to a multi-year term in exchange for a discounted
per-node rate locked for the duration of the agreement.

| Term    | Discount | Example: 3,000 nodes at Business ($120) |
|---------|----------|-----------------------------------------|
| 1 year  | 0%       | $360,000/yr                             |
| 3 years | 10%      | $324,000/yr ($972,000 total)            |
| 5 years | 15%      | $306,000/yr ($1,530,000 total)          |

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
already completed. No refund is issued for the current year.

**Loyalty discount interaction:** The multi-year discount and the
loyalty discount are separate tracks and may not be combined. Choose
whichever is more favorable at renewal. Time served under a multi-year
agreement does not count toward the loyalty clock.

## Enterprise Agreements

Enterprise agreements are available for organizations managing nodes
across multiple sites, networks, or spanner instances under a single
contract.

**Aggregate node counting:** Your tier and per-node rate are determined
by the total number of managed nodes across all sites. A single license
file is issued with the aggregate node count and deployed to the
spanner hub, which enforces the limit across all connected spokes.
Individual spokes do not require separate license files.

**Node floor:** Enterprise agreements include a minimum node commitment
across all sites. You are billed for the committed floor even if actual
aggregate usage is lower.

**Combining with multi-year:** Enterprise agreements may be combined
with multi-year commitments for both aggregate pricing and term
discounts.

Example: 15 plants, 200 nodes each = 3,000 aggregate nodes. Business
tier at $120/node = $360,000/year. With a 3-year multi-year commitment:
$324,000/year.

**Organizational changes:** Changes to organizational structure,
including acquisitions, mergers, or divestitures, require a license
amendment. Contact Alexander4, LLC to adjust your node count and issue
an updated license file.

To discuss an enterprise agreement, contact vigo.licensing@alexander4.org.

## What You Get

Every paid tier includes:

- A signed license file (`.lic`) unlocking your node count
- Access to all current and future features for the license term
- 15-second check-ins, hub-spoke spanner, AI assistant, compliance export with auto-rollback, RBAC, 100+ resource types across 8 platforms, verified binary distribution, all integrations
- The same software, the same binary — no "community" vs "enterprise" split

The Community tier (1–25 nodes) includes all features with no time limit.

## Community Tier — No Support, No Obligation

The Community tier is provided free of charge with no support obligation.
Alexander4, LLC does not guarantee any response to bug reports, questions,
or requests from Community tier users. GitHub Issues are monitored on a
voluntary, best-effort basis and may be deprioritized or closed without
resolution at any time.

Community tier users accept all risk associated with their use of the
Software. The Software is provided "AS IS" under the Community tier (and
all tiers — see LICENSE). Alexander4, LLC's total aggregate liability to
Community tier users is capped at fifty U.S. dollars ($50). If the
Software causes damage to your infrastructure, you have no claim against
Alexander4, LLC beyond this amount. See the LICENSE file for the full No
Warranty and Limitation of Liability terms.

If you require guaranteed response times, bug-fix commitments, or
configuration assistance, upgrade to a paid tier.

## Support

| Level       | Included With    | Response Time    | Channel                          |
|-------------|------------------|------------------|----------------------------------|
| Community   | Community tier   | Best effort      | GitHub Issues                    |
| Standard    | Starter, Growth  | 2 business days  | Email                            |
| Priority    | Business+        | 1 business day   | Email + direct                   |
| Dedicated   | Enterprise+      | 4 business hours | Email + direct + scheduled calls |
| Critical    | Add-on (Business+) | 4 hours, 7 days/week, 7am–7pm Pacific | Email + direct + phone |

Response times are service targets and not guaranteed SLAs. Guaranteed
SLAs with service credits are available as part of custom enterprise
agreements.

Support covers configuration guidance, bug triage, and upgrade assistance.
It does not cover writing your modules, managing your infrastructure, or
general systems administration.

## Critical Support Add-On

Available to Business tier and above (minimum annual license of
$100,000). Critical support adds extended-hours coverage on top of
your included support level.

- **Response time:** 4 hours, 7 days per week, 7:00am–7:00pm Pacific
- **Price:** 20% of annual license fee
- **Channel:** Email, direct, and phone
- **Scope:** Production-down and service-degraded issues only — not
  configuration questions or feature requests

24/7 support with guaranteed SLAs is available as a custom engagement
for Enterprise and Scale tier customers. Contact us for pricing.

## Feature Requests

All paid customers may submit feature requests via email. Requests are
reviewed and prioritized at our discretion — higher tiers receive priority
consideration, but no tier guarantees delivery of a specific feature or
timeline.

Free-tier users are welcome to suggest features via GitHub Issues.

For guaranteed delivery of a specific feature, custom development is
available as a separate engagement, scoped and quoted individually.
Contact us for details.

## Prohibited Use

This software may not be used by, for, or on behalf of:

- Any military organization, department of defense, armed forces, defense
  contractor, intelligence agency, or entity primarily engaged in military
  or weapons-related activities.
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

If your active node count exceeds your licensed limit, Vigo enters a
15-day grace period. During grace, the server operates normally but logs
a warning on every check-in. After 15 days, new node enrollments are
rejected for an additional 15 days. After 30 total days, the server
shuts down.

Existing nodes are never locked out during the grace or enrollment-block
phases. Reduce your node count or upgrade your license to return to
compliant status at any time — the grace clock resets immediately.

**Machine fingerprint mismatch:** If the server's machine fingerprint
does not match the license, the same grace period applies. This occurs
when a license file is moved to a different server or the underlying
hardware/OS changes. Contact Alexander4, LLC for a re-issued license.

**Late payment:** If payment is not received within thirty (30) days
of the due date, Alexander4, LLC will provide written notice of the
overdue balance. If the balance is not paid within fifteen (15) days
of notice, the license is suspended — new enrollments are blocked and
server access to the management UI is disabled. If payment remains
outstanding for sixty (60) days past the due date, the license
terminates. Agents continue to enforce their last-known policy during
suspension and after termination.

**Agent continuity:** If the server shuts down due to license
enforcement, all managed nodes continue to operate normally. Agents
enforce their last-known policy indefinitely without server
connectivity. Server shutdown only affects the ability to update
modules, enroll new nodes, and view the management UI. In spanner
deployments, spokes continue independent agent management if the hub
shuts down.

## Data Processing

Vigo is self-hosted software. Alexander4, LLC does not collect, process,
store, or transmit any of your data, your managed nodes' data, or your
end users' data. The Software runs entirely within your infrastructure.

**License telemetry:** The license enforcement system validates node
counts locally. No usage data, node identifiers, hostnames, IP addresses,
configuration content, or compliance results are transmitted to
Alexander4, LLC or any third party.

**Support interactions:** If you contact support and share logs,
configuration excerpts, or screenshots, that information is used solely
for resolving your issue and is not retained beyond the support case.

If you are subject to GDPR, CCPA, or similar data protection regulations,
your use of Vigo does not require a Data Processing Agreement (DPA) with
Alexander4, LLC because no personal data flows to us. You remain the sole
data controller for any personal data processed by the Software within
your environment.

## Governing Law

These commercial terms are governed by the same Governing Law, Dispute
Resolution, and Severability provisions set forth in the LICENSE file.

## Contact

Email: vigo.licensing@alexander4.org

All commercial inquiries and custom terms should be directed to this
address.
