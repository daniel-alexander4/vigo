# France Linux Migration — GTM Plan

Authored: 2026-04-18
Status: Draft — awaiting Dan's ratification

## Opportunity

The French government's directive to move public-sector offices to Linux creates a concentrated, time-bounded surge in Linux-under-management across France. Every fleet being stood up or migrated needs configuration management, compliance attestation, and remote access tooling — Vigo's core stack. Adjacent demand follows the government signal: regulated enterprises (banking, insurance, energy, healthcare) that model procurement on government standards will lean the same way over the following 12-24 months.

Vigo has never sold into France. Capturing a share of this wave with a focused, 6-9 month push is realistic because incumbents (Puppet, Chef, Salt, Ansible-at-scale) have no French-localized product and the MSP tier — the actual ICP — is not yet locked in on a winner for this wave.

## Non-Goal: Direct Government Sales

Do not pursue French government agencies directly as customers in this plan. Reasons:

- 12-18 month procurement cycles gated on ANSSI qualification and, for any hosted offering, SecNumCloud certification (6-figure cost, 12+ month timeline).
- Strong preference for French-headquartered vendors (Atos/Bull, Thales, OVHcloud, Clever Cloud) with established government relationships.
- CLOUD Act scrutiny on US-incorporated vendors touching sovereign data, even for self-hosted tools — creates a legal-review friction point no single deal is worth clearing in year one.
- Sales resource requirement (French sales rep, French legal counsel, French customer-success) sized inappropriately for pre-revenue Vigo.

Government penetration is a year-3 goal reached via MSPs that serve government accounts, not a year-1 direct push.

## Target Personas (Priority Order)

### 1. French MSPs serving public sector and mid-market (primary)

- Staffing up Linux expertise specifically to bid on government migration contracts.
- Need configuration management tooling that works across customer Linux fleets from day one.
- Already aligned with the existing MSP GTM phase in the broader pending plan — this is an acceleration of that motion scoped to France.
- Typical deal: 50-500 nodes/customer across 5-20 customers per MSP.
- Decision-maker: technical director or head of managed services. Sales cycle: 4-8 weeks with a working demo and French-localized materials.

### 2. Regulated French enterprises (secondary, year 2)

- Banks (BNP Paribas, Crédit Agricole tier), insurance (AXA, Groupama), energy (EDF, Engie), healthcare networks.
- Follow government tooling signals for compliance-defensibility reasons (ANSSI, RGS, DSPF alignment).
- Larger deals (1000-10000+ nodes) but longer cycles (6-12 months) and procurement committees.
- Pursue once MSP case studies exist — they are hard to open without reference accounts in-market.

### 3. French government agencies (not pursued directly)

Reach indirectly through MSPs. If a French MSP wins a government contract using Vigo, that becomes a potential reference/case study for eventual direct sales in year 3+.

## Competitive Landscape (France Specifically)

- **Puppet/Chef/Ansible**: present but English-first, no local support. High cost at scale (Puppet Enterprise pricing in particular). Weak compliance-reporting story for ANSSI/RGS frameworks.
- **Salt**: technical community only. No French-market commercial push.
- **Atos/Bull**: infrastructure incumbents, not config-management tool vendors. Will be a channel partner or a services wrapper around whatever tooling the customer picks, not a direct competitor.
- **Clever Cloud, Scaleway**: French PaaS/IaaS. Adjacent but not a config management competitor.
- **Home-grown scripts + Ansible**: the actual status quo for most French mid-market IT. Vigo's differentiators vs. this baseline (convergence speed, compliance mapping, P2P swarm, mTLS-native) translate cleanly.

## Product Readiness Gate

Before pursuing the first French MSP deal, the following must ship:

| Item | Owner | Effort | Status |
|---|---|---|---|
| Web UI French translation | Vigo | 2-3 weeks (i18n infra + FR catalog) | Not started |
| Marketing website French translation | Vigo | 1 week (infra reused from UI) | Not started |
| French glossary / terminology lock | Vigo | 1 day | Not started |
| Demo environment hitting Linux-flavored envoys (Debian, Ubuntu, RHEL, Rocky) | Vigo | Already covered by existing demo-environment work | In progress |
| ANSSI / RGS / CNIL compliance mapping in `server/compliance/` | Vigo | 1-2 weeks per framework; mappings only, not certification | Not started |
| GDPR / RGPD privacy impact statement in docs | Vigo | 2-3 days | Not started |
| French-language Commercial Terms + Design Partner Agreement | Vigo + legal review | Translation cheap; legal review per-jurisdiction ~$2-5K | Not started |

Soft-gate (not blocker for first deal but needed by deal 3-5):

- French-speaking first-line support (Slack response in French; can be Dan for a while, must be offloaded by 5 customers).
- A named French reference customer willing to be on a case-study page.
- VAT number registered for French sales if revenue crosses the €85K threshold.

## GTM Motion

### Phase 1 — Prepare (month 1-2)

1. **i18n infrastructure** shipped for both codebases per the pending i18n plan. French is the first translation target (not German per the earlier ranking — France's timing flip prioritizes it).
2. **ANSSI / RGS / RGPD compliance mapping** — at minimum the control-framework definitions in `server/compliance/`, even if the mapping is partial. A French MSP prospect asking "how does this help me with ANSSI?" needs a concrete page to land on.
3. **French landing page** with the localized install flow, case-study slot (placeholder for now), pricing in EUR, GDPR statement.
4. **French glossary** ratified — do not translate product names (Stockpile, Spanner, Scrier, Connwaer, Swarm, Cairn).
5. **Outreach list** — 20-30 French MSPs identified via LinkedIn, Reddit (`r/france_IT`), `CLUSIF` member directory, and French sysadmin communities. Segment by size (5-50 FTE) and public-sector exposure.

### Phase 2 — Prospect (month 2-4)

1. **Warm outreach** — LinkedIn InMail + email to named technical leaders at the outreach list. French-language message, specific reference to the Linux migration wave and how Vigo solves the multi-customer fleet problem MSPs are about to face.
2. **Content** — one or two French-language blog posts on `vigo-web` specifically addressing the Linux migration: "Managing a mixed Debian/Ubuntu/RHEL fleet with one config model", "Compliance reporting from ANSSI control families", "Why your MSP cannot scale Ansible past 500 nodes". Publish in French primarily; English mirror optional.
3. **Conferences and communities** — Paris Open Source Summit, FOSDEM (Brussels but strong French attendance), JRES (French research-ed IT conference), DevoxxFR if tangential.
4. **Target**: 3-5 signed Design Partner Agreements with French MSPs in this phase.

### Phase 3 — Convert and reference (month 4-9)

1. Design partners convert to paid after 90 days of active use.
2. First 1-2 case studies published on French landing page.
3. First French-language webinar (recorded) targeting MSPs who didn't sign during Phase 2.
4. Measure: French-originated MQLs, conversion rate versus English-originated, deal size, sales-cycle length.
5. **Gate for Phase 4 investment**: ≥3 paying French customers by end of month 9.

### Phase 4 — Scale or hold (month 9+)

- If gate met: expand to the French regulated enterprise segment (persona 2). Hire a French-fluent CSM. Open conversations about SecNumCloud if customers are requesting hosted Vigo.
- If gate missed: hold at current footprint, maintain French translations and landing page, reallocate effort to English-speaking markets. Revisit in 12 months if the French Linux wave is still cresting.

## Pricing Positioning for France

- **Community tier**: unchanged (25 nodes free).
- **Starter / Pro / Enterprise**: price in EUR at parity with USD list (not at current exchange-rate conversion — €0.93/USD would price European customers cheaper than American ones, signals weakness).
- **Multi-year discount tiers**: unchanged from the ratified 2026-04-17 pricing plan.
- **MSP partner discount**: propose a 15% MSP partner discount on Starter/Pro tiers when the MSP has 3+ customers on Vigo. This is new — add to `COMMERCIAL-TERMS.md` if adopted. Matches how Datto and ConnectWise structure partner economics; French MSPs expect this structure.

## 90-Day Action Plan

| Week | Milestone |
|---|---|
| 1 | Ratify this plan. Write French glossary. |
| 2-3 | Ship i18n infra in both codebases (mechanism pick + extraction). |
| 4 | First draft French translation of vigo-web landing + core pages. Send to reviewer. |
| 5 | Ship bilingual vigo-web (en+fr). French landing page live. |
| 6-7 | UI i18n extraction + French translation + review. |
| 8 | Ship bilingual UI. Publish first French-language blog post. |
| 9 | ANSSI control-family mappings in `server/compliance/`. |
| 10 | Outreach list built and segmented. First 10 outbound contacts sent. |
| 11-12 | Follow-up cadence. Target: 2 Design Partner conversations in motion. |

## Resources and Budget

- **Engineering**: ~6 weeks of focused i18n + compliance-mapping work. Dan or a contractor.
- **Translation review**: ~$500 for vigo-web + $1500 for UI (pair with the same reviewer for consistency). Budget $500 contingency for glossary disputes.
- **Legal**: $2-5K for French-jurisdiction review of Commercial Terms and DPA. One-time.
- **Outreach tooling**: LinkedIn Premium Business ($60/month), optional cold-outreach tool like Lemlist ($50/month for 3 months).
- **Travel**: Paris Open Source Summit attendance (December timeframe) — $1500 if attending.
- **Total phase-1 budget estimate**: ~$10-12K all-in, exclusive of Dan's time.

## Risks and Dependencies

| Risk | Likelihood | Mitigation |
|---|---|---|
| Outreach produces zero qualified conversations in phase 2 | Medium | Pre-warm via content publishing in phase 1. Gate next spend at month 9 check. |
| French MSP market consolidates around Ansible incumbent | Medium | Lean on compliance-mapping differentiation; Ansible does not auto-map controls. |
| Regulatory change (ANSSI mandate for French-headquartered tools) | Low-medium | Monitor. If it lands, partner with a French entity as reseller/distributor. |
| Translation quality complaints from prospects | Low if reviewer is qualified | Native-speaker reviewer non-negotiable. Pass-through raw-LLM output is a deal-killer for enterprise French. |
| Dan's attention pulled to other Vigo work | High | Enforce 90-day calendar blocks; kill-gate at month 9 protects against sunk-cost drift. |

## Dependencies on Existing Pending Work

- **i18n** (pending list, tightened this session): blocks phase 1 milestone weeks 2-5.
- **MSP GTM** (pending list, phase 2): France becomes the first geographic wedge of this broader motion.
- **Compliance mapping expansion**: ANSSI/RGS frameworks need entries in `server/compliance/`. Adjacent to existing HIPAA/SOC2/PCI work.
- **Pricing EUR display**: requires a small `server/license/` change to surface the right currency on the license page. Low-effort.

## Success Criteria (Measured at Month 9)

- **Must-hit (kill-gate if not met)**: 3 paying French MSP customers.
- **Target**: 5-8 paying French MSP customers, first case study published, one French-language recorded demo.
- **Stretch**: first French enterprise (non-MSP) customer in active evaluation.

If must-hit is missed, hold investment at maintenance (translations up-to-date, landing page live, no new outbound) and re-evaluate at month 18. If hit or exceeded, begin phase-4 expansion with confidence.

## Kill Criteria

Any two of the following at month 6 abort the plan and revert to maintenance:

- Zero qualified outbound conversations despite 30+ contacts.
- All Design Partner Agreements lapsed or stalled beyond 90 days.
- Reviewer feedback indicates translation quality is unsalvageable without a full-time linguist (cost escalation).
- A credible French competitor enters the market with equivalent features and local presence.
- Regulatory change explicitly excluding non-French-headquartered vendors from government-adjacent sales.
