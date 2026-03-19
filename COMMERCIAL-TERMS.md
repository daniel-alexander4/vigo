# Vigo Commercial Terms

Effective 2026-03-16. Alexander4, LLC reserves the right to update these terms.

## Licensing

Vigo uses a split-license model:

- **Agent** (`vigo`): Source-available under the Elastic License 2.0. You can read, audit, and build the agent source code. You may not offer Vigo as a managed service, redistribute modified versions, or remove license keys.
- **Server** (`vigosrv`) and **CLI** (`vigogo`): Proprietary. Distributed as compiled binaries and container images.

You may use Vigo to manage up to 25 nodes at no cost. Beyond 25 nodes,
a commercial license is required.

All features are included in every tier. There are no enterprise-only
features, gated add-ons, or premium modules. A license unlocks a node
count — nothing else changes.

## Pricing

Pricing is per managed node, per year. Your node count determines your
tier, and that tier's per-node rate applies to all your nodes.

| Tier        | Nodes            | Per Node / Year | Support Level |
|-------------|------------------|-----------------|---------------|
| Community   | 1 – 25           | Free            | Community     |
| Starter     | 26 – 250         | $144            | Standard      |
| Growth      | 251 – 1,000      | $132            | Standard      |
| Business    | 1,001 – 5,000    | $120            | Priority      |
| Enterprise  | 5,001 – 25,000   | $108            | Dedicated     |
| Scale       | 25,001 – 100,000 | $84             | Dedicated     |

Example: a 500-node fleet pays 500 × $132 = **$66,000/year**.

Billed annually. Licenses are non-refundable once issued.

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

## What You Get

Every paid tier includes:

- A signed license file (`.lic`) unlocking your node count
- Access to all current and future features for the license term
- 15-second check-ins, hub-spoke spanner, AI assistant, compliance export with auto-rollback, RBAC, all 72 resource types across 8 platforms, verified binary distribution, all integrations
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

## Prohibited Use

This software may not be used by, for, or on behalf of:

- Any military organization, department of defense, armed forces, defense
  contractor, intelligence agency, or entity primarily engaged in military
  or weapons-related activities.
- Any manufacturer, distributor, or retailer of firearms, ammunition,
  weapons, or weapons components.
- Any manufacturer, distributor, or retailer of pesticides, herbicides,
  or chemical poisons intended to kill living organisms.

No license — free or paid — will be granted for such use. These
prohibitions are absolute and non-negotiable.

## Right to Refuse

Alexander4, LLC reserves the right to decline or terminate any commercial
license for any reason, at its sole discretion, including but not limited to:

- Use cases that conflict with the values or reputation of the project
- Organizations that have previously violated the license terms
- Requests that we determine we cannot adequately support

If a license is declined, no payment is collected. If a license is
terminated, a pro-rata refund is issued for the remaining term.

## Grace Period

If your active node count exceeds your licensed limit, Vigo enters a
15-day grace period. During grace, the server operates normally but logs
a warning on every check-in. After 15 days, new node enrollments are
rejected for an additional 15 days. After 30 total days, the server
shuts down.

Existing nodes are never locked out during the grace or enrollment-block
phases. Reduce your node count or upgrade your license to return to
compliant status at any time — the grace clock resets immediately.

## Contact

Email: vigo.licensing@alexander4.org

All commercial inquiries and custom terms should be directed to this
address.
