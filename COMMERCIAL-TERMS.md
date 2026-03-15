# Hobnob Commercial Terms

Effective 2026-03-14. Alexander4, LLC reserves the right to update these terms.

## Licensing

Hobnob uses a split-license model:

- **Agent** (`hobnob`): Source-available under the Elastic License 2.0. You can read, audit, and build the agent source code. You may not offer Hobnob as a managed service, redistribute modified versions, or remove license keys.
- **Server** (`hobd`) and **CLI** (`hobnobcli`): Proprietary. Distributed as compiled binaries and container images.

You may use Hobnob to manage up to 25 nodes at no cost. Beyond 25 nodes,
a commercial license is required.

All features are included in every tier. There are no enterprise-only
features, gated add-ons, or premium modules. A license unlocks a node
count — nothing else changes.

## Pricing

Pricing is per managed node, per year, set at 75% of Puppet Enterprise
list price at every tier. Tiers are cumulative — each band prices only
the nodes in that band, so crossing a tier boundary never spikes the bill.

| Tier        | Nodes          | Per Node / Year | Example Annual Price          |
|-------------|----------------|-----------------|-------------------------------|
| Community   | 1 – 25         | $0              | $0                            |
| Starter     | 26 – 250       | $84             | 100 nodes = $6,300            |
| Growth      | 251 – 1,000    | $74             | 500 nodes = $37,400           |
| Business    | 1,001 – 5,000  | $64             | 2,500 nodes = $170,400        |
| Enterprise  | 5,001 – 25,000 | $54             | 10,000 nodes = $600,400       |
| Scale       | 25,001 – 100,000 | $41           | 50,000 nodes = $2,435,400     |
| Unlimited   | 100,001+       | $31             | Contact us                    |

Example: a 2,500-node fleet pays (225 × $84) + (750 × $74) + (1,500 × $64) = $18,900 + $55,500 + $96,000 = **$170,400/year**.

Billed annually. Licenses are non-refundable once issued.

## Loyalty Discount

Long-term customers pay less every year. After year 5, your rate
decreases automatically:

| Year  | Discount | 1K-node customer pays |
|-------|----------|-----------------------|
| 1 – 5 | 0%      | $74,400               |
| 6     | 10%     | $66,960               |
| 7     | 20%     | $59,520               |
| 8     | 30%     | $52,080               |
| 9     | 35%     | $48,360               |
| 10    | 40%     | $44,640               |
| 11+   | 45%     | $40,920               |
| Floor | 50%     | $37,200               |

The discount applies to the per-node rate at every tier. The floor is
50% — your rate will never drop below half the list price. The loyalty
clock starts from the date of your first paid license and is tied to your
organization, not a specific license term. Continuous renewal is required;
a lapse of more than 90 days resets the loyalty clock.

## What You Get

Every paid tier includes:

- A signed license file (`.lic`) unlocking your node count
- Access to all current and future features for the license term
- Hub-spoke federation, AI assistant, compliance export, RBAC, all 74 executors, all integrations
- The same software, the same binary — no "community" vs "enterprise" split

The Community tier (1–25 nodes) includes all features with no time limit.

## Support

| Level       | Included With    | Response Time    | Channel                          |
|-------------|------------------|------------------|----------------------------------|
| Community   | Community tier   | Best effort      | GitHub Issues                    |
| Standard    | Starter, Growth  | 2 business days  | Email                            |
| Priority    | Business+        | 1 business day   | Email + direct                   |
| Dedicated   | Enterprise+      | 4 business hours | Email + direct + scheduled calls |

Support covers configuration guidance, bug triage, and upgrade assistance.
It does not cover writing your modules, managing your infrastructure, or
general systems administration.

## Feature Requests

All paid customers may submit feature requests via email. Requests are
reviewed and prioritized at our discretion — higher tiers receive priority
consideration, but no tier guarantees delivery of a specific feature or
timeline.

Free-tier users are welcome to suggest features via GitHub Issues.

For guaranteed delivery of a specific feature, custom development is
available as a separate engagement, scoped and quoted individually.
Contact us for details.

## Right to Refuse

Alexander4, LLC reserves the right to decline or terminate any commercial
license for any reason, at its sole discretion, including but not limited to:

- Use cases that conflict with the values or reputation of the project
- Organizations that have previously violated the license terms
- Requests that we determine we cannot adequately support

If a license is declined, no payment is collected. If a license is
terminated, a pro-rata refund is issued for the remaining term.

## Grace Period

If your active node count exceeds your licensed limit, Hobnob enters a
15-day grace period. During grace, the server operates normally but logs
a warning on every check-in. After 15 days, new node enrollments are
rejected for an additional 15 days. After 30 total days, the server
shuts down.

Existing nodes are never locked out during the grace or enrollment-block
phases. Reduce your node count or upgrade your license to return to
compliant status at any time — the grace clock resets immediately.

## Contact

Email: hobnob.licensing@alexander4.org

All commercial inquiries, custom terms, and Unlimited tier requests
should be directed to this address.
