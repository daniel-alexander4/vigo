// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "fmt"

// apiEnvoy is the shared CLI-side representation of an envoy row from
// GET /api/v1/envoys. Callers that only need a subset of fields still
// take all of them — keeping the shape in one place means a new
// pagination envelope field, a renamed JSON tag, or a new column never
// drifts across the six different consumers (envoys list, completion,
// runs, risk, preview, dry-run).
type apiEnvoy struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	IPAddress  string `json:"ipAddress"`
	EnrolledAt string `json:"enrolledAt"`
	LastSeen   string `json:"lastSeen"`
	Revoked    bool   `json:"revoked"`
}

// apiEnvoyListPage is the server's pagination envelope on
// GET /api/v1/envoys. The fields hasMore + nextOffset drive the walk
// loop in listAllEnvoys.
type apiEnvoyListPage struct {
	Envoys     []apiEnvoy `json:"envoys"`
	Total      int32      `json:"total"`
	Offset     int32      `json:"offset"`
	Limit      int32      `json:"limit"`
	HasMore    bool       `json:"hasMore"`
	NextOffset int32      `json:"nextOffset"`
}

// envoysListClientPageSize is the limit the CLI requests per page when
// walking the full fleet. 5000 matches the server's max-cap so most
// real-world fleets fit in one round trip; larger fleets walk pages
// (sequential cost is O(N/page-size) HTTPS calls — fine for admin
// tooling, never on the hot path).
const envoysListClientPageSize = 5000

// listAllEnvoys walks every page of /api/v1/envoys and returns the
// combined slice. Set includeRevoked=true to include revoked envoys.
// Callers that need only hostnames + IDs still get the full apiEnvoy —
// the helper exists so no caller hand-rolls a pagination loop and
// silently truncates the fleet at the server's default page size.
func listAllEnvoys(client *apiClient, includeRevoked bool) ([]apiEnvoy, error) {
	var all []apiEnvoy
	offset := int32(0)
	for {
		path := fmt.Sprintf("/api/v1/envoys?limit=%d&offset=%d", envoysListClientPageSize, offset)
		if includeRevoked {
			path += "&includeRevoked=true"
		}
		var page apiEnvoyListPage
		if err := client.get(path, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Envoys...)
		if !page.HasMore {
			return all, nil
		}
		offset = page.NextOffset
	}
}
