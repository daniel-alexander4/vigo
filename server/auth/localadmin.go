// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"os"
	"strings"
)

// LoadOrCreateLocalAdminToken returns the local-admin token, generating and
// persisting it on first call. The token is the root-only credential that
// lets vigocli on the server host authenticate as admin — it replaced the
// former source-IP "peer auth", which trusted any loopback or Docker-bridge
// caller and so handed admin to non-root local users.
//
// The file is written 0600 in the bind-mounted /srv/vigo data dir: only the
// container's root (== host root) can read it, the same filesystem gate that
// already protects the secrets master key and the vigocli binary itself.
// Same generate-at-boot / persist / reload-on-restart shape as .master.key.
func LoadOrCreateLocalAdminToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading local-admin token %s: %w", path, err)
	}

	tok, _, err := GenerateAPIToken()
	if err != nil {
		return "", fmt.Errorf("generating local-admin token: %w", err)
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("writing local-admin token %s: %w", path, err)
	}
	return tok, nil
}
