// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

// Canonical Vigo pricing and support schedule. Sourced from
// COMMERCIAL-TERMS.md — update this table whenever the pricing sheet
// moves and the web UI picks it up on the next build. The on-wire
// license format in git.alexander4.io/vigo-lic intentionally carries
// only max_envoys; the matching commercial terms live with the
// product that sells them, not the cryptographic format.

// Tier is one row of the published pricing schedule plus the support
// coverage bundled with it.
type Tier struct {
	Name         string // "Community", "Starter", "Growth", ...
	MinNodes     int
	MaxNodes     int
	PricePerNode int    // annual USD per node; 0 for Community
	SupportLevel string // "Community", "Standard", "Priority", "Dedicated"
	ResponseTime string
	Channel      string
}

// Tiers lists every band in order. Keep ascending by node count so
// TierForNodes can short-circuit on the first bracket hit.
var Tiers = []Tier{
	{"Community", 1, 100, 0, "Community", "Best effort", "Email"},
	{"Starter", 101, 250, 144, "Standard", "2 business days", "Email"},
	{"Growth", 251, 1000, 132, "Standard", "2 business days", "Email"},
	{"Business", 1001, 5000, 120, "Priority", "1 business day", "Email + direct"},
	{"Enterprise", 5001, 25000, 108, "Dedicated", "4 business hours", "Email + direct + scheduled calls"},
	{"Scale", 25001, 100000, 96, "Dedicated", "4 business hours", "Email + direct + scheduled calls"},
}

// TierForNodes returns the tier whose band contains maxEnvoys. Pass
// the license's MaxEnvoys, not the current active count — the tier
// is what the customer bought, not what they're using today. Fleets
// beyond the published Scale ceiling fall through to Scale.
func TierForNodes(maxEnvoys int) *Tier {
	for i := range Tiers {
		if maxEnvoys >= Tiers[i].MinNodes && maxEnvoys <= Tiers[i].MaxNodes {
			return &Tiers[i]
		}
	}
	return &Tiers[len(Tiers)-1]
}

// SupportContactEmail is the single inbox for every support level.
// Split into per-tier addresses if volume ever justifies it.
const SupportContactEmail = "vigo.support@alexander4.io"

// CriticalSupportAddOn describes the extended-hours opt-in available
// on top of any paid tier at Growth or above. Not encoded in the
// license payload today — surfaced as a contact-us callout.
var CriticalSupportAddOn = struct {
	EligibleFrom string
	ResponseTime string
	Hours        string
	Channel      string
	Pricing      string
}{
	EligibleFrom: "Growth",
	ResponseTime: "4 hours",
	Hours:        "7 days/week, 7:00am – 7:00pm Pacific",
	Channel:      "Email + direct + phone",
	Pricing:      "20% of annual license fee",
}
