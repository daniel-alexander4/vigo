
---
name: Go-to-Market Channels by Industry
description: Specific strategies to reach buyers in each target market — communities, events, content, partnerships, and outreach tactics
type: project
---

# Go-to-Market Channels by Industry

Created 2026-03-19. Companion to [market_analysis.md](market_analysis.md) and [market_15s_convergence.md](market_15s_convergence.md).

---

## 1. Payment Processing (PCI DSS)

### Where the buyers are
- **PCI Security Standards Council** — publishes the standard, runs events, has a directory of QSAs and participating organizations
- **Conferences:** PCI Community Meetings (North America + Europe, annual), RSA Conference, Black Hat (compliance track)
- **QSA firms** — the auditors are the gatekeepers. If a QSA recommends your tool, the client buys it. There are ~400 QSA companies globally.
- **LinkedIn:** Search "PCI compliance manager," "QSA," "payment security." Small world — maybe 5,000 people globally who make these decisions.

### Strategy
1. **Build a PCI DSS 4.0 requirement mapping document** — free PDF download, gated with email. Map every relevant Vigo capability to specific PCI requirements (10.7.2, 11.5, 6.3, etc.). This becomes the thing QSAs share with their clients.
2. **Partner with 2-3 QSA firms** — offer them free licenses for their internal use and a referral fee. They recommend Vigo to clients who need continuous monitoring tooling. QSAs evaluate dozens of companies per year. Each QSA firm is a channel.
3. **Write for PCI-focused publications** — PCI Perspectives (the Council's blog accepts guest posts), SecurityWeek, Dark Reading. Topic: "PCI DSS 4.0's Continuous Monitoring Requirement: What It Actually Means Technically."
4. **Sponsor a PCI Community Meeting table** — ~$5-10K. You're in a room with 500 payment security professionals for two days. Bring the demo laptop.
5. **LinkedIn outreach to payment ISVs** — companies building payment software (not Visa/Mastercard — their vendors). They need to prove their infrastructure is compliant to onboard with card networks.

### First move
Write the PCI DSS 4.0 mapping doc. It costs nothing, establishes authority, and is the thing you hand to every QSA you talk to.

---

## 2. Online Gambling & Casino Operations

### Where the buyers are
- **Conferences:** ICE London (40,000 attendees, largest gambling industry event), G2E Las Vegas (Global Gaming Expo), SiGMA (Malta)
- **Regulators publish licensee lists** — Nevada Gaming Control Board, Malta Gaming Authority, UK Gambling Commission all publish lists of licensed operators. That's your prospect list.
- **iGaming forums:** SBC (Sports Betting Community), GPWA (Gambling Portal Webmasters Association), CalvinAyre.com
- **LinkedIn:** "Head of Platform," "VP Engineering" at gambling companies. The titles are consistent.
- **Malta** — the global hub for online gambling. Most European operators are headquartered there. A single trip to Malta puts you in front of dozens of potential customers.

### Strategy
1. **Build a gaming commission compliance mapping** — map Vigo capabilities to specific regulatory requirements from Nevada, Malta (MGA), and UKGC. Each jurisdiction has published technical standards for system integrity.
2. **Target the platform providers first** — companies like Kambi, SBTech, EveryMatrix, and Playtech provide the platform that dozens of operators run on. One platform sale = many operators using Vigo. These companies have 50-500 servers each.
3. **Attend ICE London** (February, annually) — a booth is expensive (~$20-50K) but you don't need one. Attend, walk the floor, talk to CTOs. The gambling industry is small and relationship-driven.
4. **Cold outreach via LinkedIn to Malta-based CTOs** — the iGaming industry in Malta is tight-knit. One introduction leads to five. Message: "I built a tool that gives you provable system integrity for your MGA license renewal. 15-second check-ins, signed audit trail, one-click export for your compliance officer."
5. **Publish a case study with one operator** — gambling companies talk to each other. One visible deployment creates FOMO. Offer the first operator free licensing in exchange for a named case study.

### First move
Research the MGA technical standards (publicly available) and build the mapping doc. Then identify 10 Malta-based iGaming CTOs on LinkedIn and start conversations.

---

## 3. High-Frequency Trading / Market Makers

### Where the buyers are
- **Not at conferences** — HFT firms are secretive. They don't attend trade shows or publish blog posts.
- **Recruiting networks** — HFT firms recruit from the same pool. Engineers who've worked at Citadel, Jump Trading, Jane Street, Two Sigma, Hudson River Trading, Virtu, and Tower Research rotate between firms. They bring tools with them.
- **Hacker News / Lobsters** — HFT engineers read these. A well-written technical post about Vigo's architecture (Rust agent, 0.5ms server overhead, mTLS) will reach them.
- **Quantitative finance meetups** — QuantMinds, Battle of the Quants, local quant meetups in NYC, Chicago, London.
- **Infrastructure-focused hedge fund conferences** — HFT firms care about infrastructure more than most.

### Strategy
1. **Write a deeply technical blog post** — "Sub-Second Configuration Convergence: Architecture of a Rust-Based State Enforcement Agent." Publish on your blog, submit to Hacker News and Lobsters. HFT engineers self-select based on technical quality. No marketing fluff — they'll close the tab.
2. **Open source the agent** (or keep Elastic-2.0 visible) — HFT firms won't adopt anything they can't audit. The Rust agent source being visible is a feature, not a risk.
3. **Target infrastructure teams, not traders** — the buyer is the "Head of Infrastructure" or "Site Reliability" person. They report to the CTO, not the trading desk.
4. **Referral through infra consultants** — firms like Corral Consulting, Aldrin Solutions, and Headlands Technologies do infrastructure consulting for trading firms. Partner with them.
5. **Price aggressively** — HFT firms will pay $50-100/node/month. Don't discount. The value prop is "your risk controls are continuously enforced and provable to the SEC."

### First move
Write the technical blog post. If it's good enough, the right people will find you. HFT is a pull market, not a push market.

---

## 4. MSSPs (Managed Security Service Providers)

### Where the buyers are
- **Reddit:** r/msp (150K+ members) — the single most active community of MSP owners on the internet. They discuss tools daily.
- **Conferences:** IT Nation Connect (ConnectWise's annual event, 3,000+ MSPs), DattoCon, Robin Robins' Producers Club events, ASCII events, The 20 peer group meetings
- **Peer groups:** The 20, HTG (now ConnectSMART), ASCII Group, TruMethods — MSPs join these for vendor recommendations and peer advice. A recommendation from a peer group member is worth 10 cold emails.
- **Podcasts:** MSP Voice, The 20 Podcast, Kaseya's Growth Summit recordings, Paul Dippell (Service Leadership)
- **Vendor marketplaces:** ConnectWise Marketplace, Datto Marketplace, NinjaRMM integrations page

### Strategy
1. **Post on r/msp** — not a sales pitch. A conversation: "I built a compliance enforcement tool for MSPs. 25 nodes free. Looking for 3 MSPs to pilot and give feedback." r/msp respects builders, hates vendors. Be authentic.
2. **Attend IT Nation Connect** (November, annually, Orlando) — the largest MSP event. Don't sponsor ($50K+). Attend, run a "birds of a feather" session on compliance automation, collect contacts.
3. **Build a ConnectWise Manage webhook integration** — when Vigo detects drift, auto-create a ticket in ConnectWise. This is table stakes for MSP adoption. Document it well.
4. **Offer free 90-day pilots to 3 MSPs** — hand-hold them through setup. Their feedback shapes the product. Their success stories become case studies.
5. **Guest on MSP podcasts** — MSP Voice and similar shows are always looking for guests. Topic: "Compliance-as-a-Service: How to Turn Configuration Management Into MRR."
6. **Join an ASCII event** — small, regional, MSP-focused. You can present to 30-50 MSP owners in a room. No booth needed, just a sponsor slot for a 15-minute presentation ($2-5K).
7. **Create an MSP ROI calculator** — "How much time does your team spend on compliance evidence collection? How many hours per client per audit? Vigo reduces that to zero." Put it on the website.

### First move
r/msp post + 3 free pilots. This is the cheapest, fastest path to real users and real feedback.

---

## 5. Healthcare (HIPAA)

### Where the buyers are
- **Conferences:** HIMSS (40,000+ attendees, the largest health IT event), CHIME Fall Forum (CIO-level), Health IT Summit
- **HITRUST** — the certification body. HITRUST CSF-certified organizations are the ones who care most about continuous compliance tooling. HITRUST publishes a list of assessors.
- **Healthcare ISACs** — Health-ISAC (Information Sharing and Analysis Center) is a membership org for healthcare security teams. They share threat intel and tool recommendations.
- **LinkedIn:** "HIPAA Security Officer," "Healthcare CISO," "IT Director" at hospital systems
- **Healthcare IT publications:** Healthcare IT News, CHIME, KLAS Research (if you get rated by KLAS, health systems will find you)

### Strategy
1. **Build HIPAA Security Rule mapping** — map Vigo to 45 CFR 164.312 (technical safeguards). Focus on access controls, audit controls, integrity controls, transmission security.
2. **Build pre-built HIPAA hardening modules** — `.vgo` modules that enforce CIS benchmarks for healthcare (Ubuntu, RHEL, Windows Server) mapped to HIPAA requirements. Give them away. They're your Trojan horse.
3. **Partner with a HITRUST assessor** — same model as QSA partnerships for PCI. Assessors recommend tools. There are ~100 HITRUST assessor firms.
4. **Target mid-size hospital systems (5-20 hospitals)** — large systems (Kaiser, HCA) have procurement cycles measured in years. Mid-size systems have budget authority and urgency. They're also underserved by enterprise tools.
5. **Attend HIMSS** (March/April, annually) — expensive for a booth, but walk the floor. The compliance/security pavilion is where your buyers are. Bring a tablet with a live demo.
6. **Publish in Healthcare IT News** — they accept contributed articles. Topic: "Beyond Patch Management: Continuous Configuration Compliance for HIPAA."

### First move
Build the HIPAA mapping doc and 3-5 HIPAA hardening modules. Publish them as a free "HIPAA Compliance Starter Kit" with email gate.

---

## 6. Critical Infrastructure / Energy (NERC CIP)

### Where the buyers are
- **NERC publishes a registry** of all registered entities (utilities, ISOs, RTOs). That's your prospect list — about 1,900 entities.
- **Conferences:** GridSecCon (NERC's own security conference), EnergySec Summit, SANS ICS Summit, S4 Conference (ICS/SCADA security)
- **ISACs:** E-ISAC (Electricity Information Sharing and Analysis Center) — membership org for electric utilities
- **Consulting firms:** Dragos, Claroty, Nozomi Networks do ICS security consulting. They recommend tools.
- **NERC auditors** — regional entities (RF, SERC, WECC, etc.) conduct CIP audits. Their auditors influence tooling decisions.

### Strategy
1. **Build NERC CIP-010 mapping** — CIP-010-4 (Configuration Change Management and Vulnerability Assessments) is the most directly relevant standard. Map every requirement to a Vigo capability.
2. **Target cooperative utilities first** — electric cooperatives (about 900 in the US) are smaller, more agile, and less bureaucratic than IOUs (investor-owned utilities). Many are struggling with CIP compliance on tight budgets.
3. **Partner with an ICS security consultancy** — Dragos, Claroty, or a smaller firm. They're already in the door at utilities doing assessments. Vigo solves a problem they identify but can't fix with their own tools.
4. **Attend GridSecCon** (October, annually) — 500-800 utility security professionals. This is the most targeted event possible. NERC runs it. Everyone there has CIP compliance on their plate.
5. **Publish a whitepaper on CIP-010 automation** — "Automating NERC CIP-010: Continuous Configuration Monitoring for Bulk Electric System Cyber Assets." Submit to EnergySec for distribution.
6. **Get on the E-ISAC radar** — present at an E-ISAC webinar. They host monthly sessions on security topics. A tool that automates CIP-010 compliance would be highly relevant.

### First move
NERC CIP-010 mapping document. Then identify 5 cooperative utilities in your region (Pacific Northwest) and reach out directly. Co-ops are accessible — the CISO is often also the IT director and answers their own email.

---

## 7. Cryptocurrency Exchanges & Custodians

### Where the buyers are
- **Crypto Twitter / X** — the industry lives here. CTOs and security engineers are active and responsive.
- **Conferences:** Consensus (CoinDesk, largest US crypto event), ETHDenver, Token2049 (Singapore), Permissionless
- **Telegram/Discord** — many exchanges run public engineering channels
- **Job boards** — Crypto.jobs, Web3.career, AngelList crypto filter. Companies actively hiring infrastructure engineers have budget and scaling problems Vigo solves.
- **Security auditors** — Trail of Bits, OpenZeppelin, Halborn, Cure53 audit exchange infrastructure. They recommend operational security tooling.

### Strategy
1. **Write a blog post: "Infrastructure Integrity for Digital Asset Custody"** — frame Vigo as the tool that prevents the next Mt. Gox. Technical audience. Focus on mTLS, ED25519 signatures, secret handling, offline convergence.
2. **Target Tier 2 exchanges first** — not Coinbase or Kraken (massive procurement). Target Gemini, Bitstamp, Bitso, Rain Financial, MEXC — large enough to have real infrastructure, small enough to make fast decisions.
3. **Security auditor partnerships** — Trail of Bits and Halborn do infrastructure security audits for exchanges. A recommendation from them carries enormous weight. Offer them free licenses for their own internal use.
4. **Attend ETHDenver or Token2049** — these are developer-heavy events where infrastructure engineers attend. No booth needed. Host a side event: "Infrastructure Security for Exchanges" — pizza, beer, 20-minute demo.
5. **Contribute to crypto security standards** — CCSS (Cryptocurrency Security Standard) is maintained by CryptoCurrency Certification Consortium (C4). Participating in standards work builds credibility and relationships.
6. **Open-source a "crypto hardening" module pack** — pre-built .vgo modules for HSM configuration, signing server lockdown, network segmentation. Give it away on GitHub. Exchanges will find it.

### First move
Blog post + crypto hardening module pack on GitHub. The crypto community adopts tools they find on GitHub and Twitter, not through enterprise sales processes.

---

## 8. SaaS Providers (SOC 2)

### Where the buyers are
- **Everywhere** — every B2B SaaS company needs SOC 2. There are thousands of potential customers.
- **SOC 2 compliance platforms:** Vanta, Drata, Secureframe, Laika, Thoropass — these are partners, not competitors. They help companies get SOC 2 but don't enforce configuration. Vigo fills their gap.
- **Startup communities:** Y Combinator (portfolio companies need SOC 2 to sell to enterprise), TechStars, startup Slack communities
- **DevOps communities:** DevOps subreddits, CNCF Slack, HashiCorp forums, Hacker News
- **LinkedIn:** "Head of Security," "Compliance Manager" at Series A-C SaaS companies

### Strategy
1. **Integrate with Vanta/Drata** — these platforms have thousands of customers. An integration that feeds Vigo compliance data into Vanta/Drata as continuous evidence would be a massive distribution channel. Reach out to their partnerships teams.
2. **Write "SOC 2 Type II Without the Scramble"** — blog post targeting startup CTOs who just got their first enterprise customer asking for SOC 2. Topic: how Vigo automates the infrastructure evidence that SOC 2 auditors want.
3. **Y Combinator outreach** — YC companies famously need SOC 2 to close enterprise deals. Post in YC's internal forums (if you can get access through a YC alum) or target YC companies on LinkedIn.
4. **Hacker News launch** — "Show HN: 15-Second Configuration Drift Detection." The SOC 2 angle is secondary; lead with the technical story. SaaS CTOs on HN will connect the dots.
5. **Free tier is the wedge** — 25 nodes free covers most early-stage SaaS companies' entire infrastructure. They start free, grow into paid as they scale. No sales motion needed for initial adoption.
6. **Content marketing: SOC 2 evidence guide** — "The SOC 2 Infrastructure Evidence Checklist: What Your Auditor Actually Wants." Gated download. Every startup CTO googling "SOC 2 compliance" finds it.

### First move
Hacker News "Show HN" post. Lead with the technical architecture (Rust agent, 15-second convergence, 0.5ms overhead). SOC 2 SaaS founders will self-identify.

---

## Cross-Market Channels (Works Everywhere)

### Content Marketing
- **Technical blog** — one deep post per month. Architecture, benchmarks, compliance mappings. Publish on your own blog, cross-post to Hacker News, Lobsters, relevant subreddits.
- **YouTube** — 3-minute demo videos per industry. "Vigo for PCI DSS," "Vigo for MSPs," "Vigo for Healthcare." Short, no fluff, show the product.
- **Compliance mapping documents** — one per framework (PCI, HIPAA, SOC 2, NERC CIP, CMMC, GDPR). Gated PDFs. These are your highest-value lead magnets.

### Developer Relations
- **GitHub presence** — agent source visible (Elastic-2.0), example modules in a public repo, community contributions welcome
- **Discord/Slack community** — start small. 50 active users is better than 5,000 silent ones.
- **Conference talks** — submit CFPs to DevOpsDays, SCALE, FOSDEM, BSides (security). Talk about the architecture, not the product.

### Partnerships
- **Compliance platforms** (Vanta, Drata, Secureframe) — integration partners
- **Auditor firms** (QSAs, HITRUST assessors, SOC 2 auditors) — referral partners
- **ICS security consultancies** (Dragos, Claroty) — referral partners
- **MSP peer groups** (The 20, ASCII, HTG) — channel partners

### Pricing as Distribution
- **25-node free tier** — removes friction for every market. Let them start without a sales call.
- **Free compliance modules** — HIPAA, PCI, CIS hardening packs. Give away the modules, sell the platform.
- **"First client free" for MSPs** — MSPs pilot with their most demanding client. Success = rollout across all clients.

---

## Priority Sequence (Solo Founder)

You can't pursue all markets at once. Recommended order based on effort-to-impact ratio:

| Priority | Market | First Move | Cost | Timeline to First Customer |
|---|---|---|---|---|
| 1 | MSSPs | r/msp post + 3 pilots | $0 | 1-2 months |
| 2 | SaaS/SOC 2 | Hacker News Show HN | $0 | 1-3 months |
| 3 | Crypto exchanges | Blog post + GitHub modules | $0 | 2-4 months |
| 4 | Online gambling | Malta CTO outreach | $200 (LinkedIn Premium) | 3-6 months |
| 5 | Healthcare | HIPAA mapping + HIMSS attendance | $2-5K | 6-12 months |
| 6 | Payment/PCI | QSA partnerships + PCI mapping | $2-5K | 6-12 months |
| 7 | Energy/NERC CIP | CIP-010 mapping + co-op outreach | $1-3K | 6-18 months |
| 8 | HFT/Trading | Technical blog post, wait | $0 | Unpredictable |
