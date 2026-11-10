// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"errors"
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
//
// An existing file is trusted only if that gate still holds: a regular
// file (not a symlink), owned by this process's uid, with no group or other
// permission bits, holding a vgot_ token. Anything else is refused and the
// server does not start — a token another user could read, or could have
// planted, is not a root-only credential. The operator fixes the mode or
// deletes the file (a fresh token is minted on the next boot).
func LoadOrCreateLocalAdminToken(path string) (string, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		return loadLocalAdminToken(path, info)
	case !os.IsNotExist(err):
		return "", fmt.Errorf("reading local-admin token %s: %w", path, err)
	}

	tok, _, err := GenerateAPIToken()
	if err != nil {
		return "", fmt.Errorf("generating local-admin token: %w", err)
	}
	// O_EXCL: never write through a file (or symlink) that appeared after
	// the Lstat above.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("creating local-admin token %s: %w", path, err)
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("writing local-admin token %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("writing local-admin token %s: %w", path, err)
	}
	return tok, nil
}

func loadLocalAdminToken(path string, info os.FileInfo) (string, error) {
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("local-admin token %s is not a regular file (%s); remove it and restart to mint a new one", path, info.Mode().Type())
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("local-admin token %s has mode %04o; it must not be readable by group or other — run `chmod 600 %s`, or remove it and restart to mint a new one", path, perm, path)
	}
	uid, ok := fileOwnerUID(info)
	if !ok {
		return "", fmt.Errorf("local-admin token %s: cannot determine file owner", path)
	}
	if want := os.Geteuid(); uid != want {
		return "", fmt.Errorf("local-admin token %s is owned by uid %d, not the server's uid %d; remove it and restart to mint a new one", path, uid, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading local-admin token %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(data))
	if !strings.HasPrefix(tok, apiTokenPrefix) || len(tok) <= len(apiTokenPrefix) {
		return "", errors.New("local-admin token " + path + " does not hold a vgot_ token; remove it and restart to mint a new one")
	}
	return tok, nil
}
