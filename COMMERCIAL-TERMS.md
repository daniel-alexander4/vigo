# Vigo Commercial Terms

Vigo is a distributed state enforcement engine for managing infrastructure
at scale. It replaces ad-hoc scripts and legacy configuration management
tools with a single system that enforces desired state, tracks compliance
against regulatory frameworks, and provides real-time fleet visibility —
all self-hosted, with no external dependencies.

Effective 2026-03-16. Last updated 2026-05-20. Alexander4, LLC reserves the right to update these
terms. Changes apply to new license terms and renewals only. Existing
agreements, including multi-year commitments, are governed by the terms
in effect at the time of signing for the duration of that agreement.

## Licensing

Vigo is proprietary software. Copyright (c) 2026 Alexander4, LLC.
All rights reserved. See LICENSE for the full license text.

You may use Vigo to manage up to 100 nodes at no cost. Beyond 100 nodes,
a commercial license is required.

All features are included in every tier. There are no enterprise-only
features, gated add-ons, or premium modules. A license unlocks a node
count — nothing else changes.

**Community tier stability:** The Community tier node limit may change
over time. Any decrease in the Community tier node limit will not apply
to users who were actively using the Software under the Community tier
before the change. Such users retain the higher limit for as long as
they continuously use the Software.

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
node, per year, in graduated brackets: each bracket's per-node rate
applies only to the nodes that fall within that bracket. The first 100
nodes are always free, no matter how large the fleet grows.

| Tier        | Bracket          | Per Node / Year | Support Level |
|-------------|------------------|-----------------|---------------|
| Community   | 1 – 100          | Free            | Community     |
| Starter     | 101 – 250        | $144            | Standard      |
| Growth      | 251 – 1,000      | $132            | Standard      |
| Business    | 1,001 – 5,000    | $120            | Priority      |
| Enterprise  | 5,001 – 25,000   | $108            | Dedicated     |
| Scale       | 25,001 – 100,000 | $96             | Dedicated     |

Your support level is set by the highest bracket your fleet straddles —
a 500-node fleet has Standard support (Growth bracket), regardless of
how many nodes are in the Community and Starter brackets below.

Worked examples:

- **500 nodes:** 0 (1–100) + 150 × $144 (101–250) + 250 × $132 (251–500) = **$54,600/year**, Standard support.
- **3,000 nodes:** 0 + 150 × $144 + 750 × $132 + 2,000 × $120 = **$360,600/year**, Priority support.
- **25,000 nodes:** 0 + 150 × $144 + 750 × $132 + 4,000 × $120 + 20,000 × $108 = **$2,760,600/year**, Dedicated support.

For fleets exceeding 100,000 nodes, contact vigo.licensing@alexander4.io
for custom pricing.

Billed annually. Payment is due within thirty (30) days of invoice
date (Net 30). Custom payment terms may be negotiated as part of an
enterprise agreement.

**Scale-down:** You may request a reduced node count at any time. An
updated license file will be issued for the lower count. Reductions
apply prospectively only — no refund is issued for the current term,
and the reduced rate takes effect at the next billing cycle.
Multi-year agreements with a node floor cannot be reduced below the
committed floor.

Refund policy: if you are unsatisfied within the first
thirty (30) days of a new license term, you may request a full refund.
After thirty days, licenses are non-refundable for the remainder of the
term, except where required by applicable law (including EU consumer
protection regulations) or as expressly provided in the Right to Refuse
section below. Any refund applies only to the license fee and excludes
taxes, third-party costs, and separately negotiated services.

## Loyalty Discount

Long-term customers pay less every year. Starting in year 3, your rate
decreases by 3% per year from 100% down to a 75% floor in year 12. The
discount is a multiplier on the graduated bill — the bracket math
underneath does not change, the total is just scaled down.

| Year   | You Pay | 1K-node Growth customer pays |
|--------|---------|------------------------------|
| 1 – 2  | 100%   | $120,600                     |
| 3      | 97%    | $116,982                     |
| 4      | 94%    | $113,364                     |
| 5      | 91%    | $109,746                     |
| 6      | 88%    | $106,128                     |
| 7      | 85%    | $102,510                     |
| 8      | 82%    | $98,892                      |
| 9      | 79%    | $95,274                      |
| 10     | 76%    | $91,656                      |
| 11     | 75%    | $90,450                      |
| 12+    | 75%    | $90,450                      |

The loyalty floor is 75% — your loyalty rate will never drop below
three-quarters of the list price. Multi-year rates lock independently
of the loyalty curve and may go further during the term (see Multi-Year
Commitments). The loyalty clock starts from the date of your first paid
license and is tied to your organization, not a specific license term.
Continuous renewal is required; a lapse of more than 90 days resets the
loyalty clock.

## Multi-Year Commitments

Customers may commit to a multi-year term in exchange for a discounted
list-price bill locked for the duration of the agreement. The discount
is a multiplier on the graduated bracket total.

| Term    | Discount | Example: 3,000 nodes (graduated list = $360,600) |
|---------|----------|--------------------------------------------------|
| 1 year  | 0%       | $360,600/yr                                      |
| 3 years | 15%      | $306,510/yr ($919,530 total)                     |
| 5 years | 22%      | $281,268/yr ($1,406,340 total)                   |
| 7 years | 27%      | $263,238/yr ($1,842,666 total)                   |

**Payment:** Billed annually at the start of each contract year at the
locked discounted rate.

**Rate lock:** The discounted bracket schedule is fixed for the full
term, regardless of any list price changes during the commitment period.

**Node floor:** Multi-year agreements include a minimum node commitment.
You are billed at the locked discounted graduated total for the
committed node count even if actual usage is lower. Nodes above the
floor are added to the graduated total at the same locked discount.

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
advances your loyalty clock by the length of the term: a 3-year deal
grants 3 years of loyalty credit at signing, a 5-year deal grants 5
years, a 7-year deal grants 7 years. Credit is applied at signing,
accumulates across consecutive multi-year commitments, and is tied to
your organization. Committed customers therefore reach the 75% loyalty
floor years earlier than annual-renewing customers.

Example: a 1,000-node Growth customer (graduated list = $120,600/year)
signs a 5-year deal. For years 1–5 they pay the locked 22%-off rate —
**$94,068/year**. At the first annual renewal (year 6), their loyalty
clock reads year 10 (5 elapsed + 5 credit), so their rate is 24% off —
**$91,656/year**. From year 7 onward they are at the 75% loyalty floor
— **$90,450/year**.

## Enterprise Agreements

Enterprise agreements are available for organizations managing nodes
across multiple sites, networks, or spanner instances under a single
contract.

**Aggregate node counting:** Your tier and graduated bracket total are
determined by the total number of managed nodes across all sites. A
single license file is issued with the aggregate node count and
deployed to the spanner hub, which enforces the limit across all
connected bolts. Individual bolts do not require separate
license files.

**Node floor:** Enterprise agreements include a minimum node commitment
across all sites. You are billed for the committed floor even if actual
aggregate usage is lower.

**Combining with multi-year:** Enterprise agreements may be combined
with multi-year commitments for both aggregate pricing and term
discounts.

Example: 15 plants, 200 nodes each = 3,000 aggregate nodes. Graduated
total: 0 + 150 × $144 + 750 × $132 + 2,000 × $120 = **$360,600/year**
at Priority support. With a 3-year multi-year commitment (15% off):
**$306,510/year**.

**Organizational changes:** Changes to organizational structure,
including acquisitions, mergers, or divestitures, require a license
amendment, and continued use of the Software following such changes
without an updated license constitutes use outside the scope of the
license. Contact Alexander4, LLC to adjust your node count and issue
an updated license file.

To discuss an enterprise agreement, contact vigo.licensing@alexander4.io.

## What You Get

Every paid tier includes:

- A signed license file (`.lic`) unlocking your node count
- Access to all current and future features for the license term
- 15-second check-ins, hub-spoke spanner federation, AI assistant, compliance export with auto-rollback, RBAC, 73 resource types across 8 platforms, verified binary distribution, all integrations
- The same software, the same binary — no "community" vs "enterprise" split

The Community tier (1–100 nodes) includes all features with no time limit.

## Evaluation Licenses

If you need to evaluate Vigo beyond 100 nodes before committing to a
paid license, Alexander4, LLC may issue a 90-day evaluation license at
its discretion. Evaluation licenses unlock a higher node count for the
evaluation period and automatically revert to the Community tier (100
nodes) upon expiry. No payment is required. To request an evaluation
license, contact vigo.licensing@alexander4.io with your use case and
expected node count.

## Community Tier — No Support, No Obligation

The Community tier is provided free of charge with no support obligation.
Alexander4, LLC does not guarantee any response to bug reports, questions,
or requests from Community tier users. Community tier email is monitored
on a voluntary, best-effort basis and messages may be deprioritized or
left unanswered at any time.

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
| Community   | Community tier   | Best effort      | Email                            |
| Standard    | Starter, Growth  | 2 business days  | Email                            |
| Priority    | Business+        | 1 business day   | Email + direct                   |
| Dedicated   | Enterprise+      | 4 business hours | Email + direct + scheduled calls |
| Critical    | Add-on (Growth+) | 4 hours, 7 days/week, 7am–7pm Pacific | Email + direct + phone |

Response times are service targets only and do not create any
warranty, guarantee, or service level commitment unless expressly set
forth in a separate written agreement. Guaranteed SLAs with service
credits are available only if expressly set forth in a separate
written enterprise agreement.

Support covers configuration guidance, bug triage, and upgrade assistance.
It does not cover writing your modules, managing your infrastructure, or
general systems administration.

## Critical Support Add-On

Available to Growth tier and above. Critical support adds
extended-hours coverage on top of your included support level.

- **Response time:** 4 hours, 7 days per week, 7:00am–7:00pm Pacific
- **Price:** 20% of annual license fee
- **Channel:** Email, direct, and phone
- **Scope:** Production-down and service-degraded issues only — not
  configuration questions or feature requests

24/7 support with guaranteed SLAs is available only if expressly set
forth in a separate written enterprise agreement for Enterprise and
Scale tier customers. Contact us for pricing.

## Feature Requests

All paid customers may submit feature requests via email. Requests are
reviewed and prioritized at our discretion — higher tiers receive priority
consideration, but no tier guarantees delivery of a specific feature or
timeline.

Free-tier users are welcome to suggest features via email.

For guaranteed delivery of a specific feature, custom development is
available as a separate engagement, scoped and quoted individually.
Contact us for details.

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
license for any reason, at its sole discretion, subject to applicable
law, including but not limited to:

- Use cases that conflict with the values or reputation of the project
- Organizations that have previously violated the license terms
- Requests that we determine we cannot adequately support

If a license is declined before issuance of the license, no payment
is collected.

If a license is terminated by Alexander4, LLC due to your violation of
the license terms (including Use Prohibitions), no refund is issued
and any applicable multi-year early termination clawback applies.

If a license is terminated by Alexander4, LLC for reasons unrelated
to a violation (values, reputation, inability to support), a pro-rata
refund is issued for the remaining term and no early termination
clawback applies.

## Enforcement

**Node limit reached:** If you attempt to enroll a new envoy while your
active fleet is at or above the licensed limit, the enrollment is
rejected immediately. There is no grace period for accidental
over-enrollment. Existing envoys continue to check in, converge, and
report normally. Upgrade your license or decommission existing nodes
to return to compliance.

**License expiry, downgrade, or fingerprint mismatch:** If your license
expires, a smaller license is installed on a fleet that already exceeds
its cap, or the server's machine fingerprint no longer matches the
license, Vigo enters a 30-day staged grace period:

- **Days 1–15 (Grace Period):** the server operates normally; a warning
  is logged on every check-in and surfaced in the management UI.
- **Days 16–30 (Enrollment Block):** new envoy enrollments are rejected;
  existing envoys continue to check in, converge, and report; the
  management UI continues to operate.
- **After day 30 (Service Stop):** the server shuts down. Existing
  envoys continue enforcing their last-known policy indefinitely
  without server connectivity.

Returning to compliant status at any point during the staged grace
period resets the clock immediately. Paid license files are bound to a
machine fingerprint derived from the server's machine ID, primary
network interface, and TLS CA certificate — moving a license to a
different server or reinstalling the OS will trigger the staged grace
above. Contact Alexander4, LLC for a re-issued license at no charge.

**Late payment:** If payment is not received within thirty (30) days
of the due date, Alexander4, LLC will provide written notice of the
overdue balance. If the balance is not paid within fifteen (15) days
of notice, the license is suspended — new enrollments are blocked and
server access to the management UI is disabled. If payment remains
outstanding for sixty (60) days past the due date, the license
terminates, and you remain responsible for all amounts due for the
applicable license term. Agents continue to enforce their last-known
policy during suspension and after termination, but Alexander4, LLC
has no obligation to provide updates, support, or continued access to
the management UI during any period of suspension or following
termination.

**Agent continuity:** If the server shuts down due to license
enforcement, all managed nodes continue to operate normally. Agents
enforce their last-known policy indefinitely without server
connectivity. Server shutdown only affects the ability to update
modules, enroll new nodes, and view the management UI. In spanner
deployments, bolts continue independent agent management if the hub
shuts down.

**Technical enforcement:** License enforcement actions described in
this section are implemented through the Software's technical controls
and are not intended as a penalty or liquidated damages.

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
your environment, and you are solely responsible for determining whether
your use of the Software complies with applicable data protection,
security, and regulatory requirements.

## Governing Law

These commercial terms are governed by the same Governing Law, Dispute
Resolution, and Severability provisions set forth in the LICENSE file.

## Contact

Email: vigo.licensing@alexander4.io

All commercial inquiries and custom terms should be directed to this
address.
