// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateEphemeralSSHKey creates an ED25519 keypair for a single Scrier session.
// Returns the PEM-encoded private key (for guacd) and the OpenSSH authorized_keys
// format public key (for the agent to install temporarily).
func GenerateEphemeralSSHKey() (privateKeyPEM []byte, authorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate ed25519 key: %w", err)
	}

	// PEM-encode the private key for guacd's "private-key" connect param.
	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, "", fmt.Errorf("marshal private key: %w", err)
	}
	privateKeyPEM = pem.EncodeToMemory(privBytes)

	// OpenSSH authorized_keys format for the agent.
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", fmt.Errorf("convert to ssh public key: %w", err)
	}
	authorizedKey = string(ssh.MarshalAuthorizedKey(sshPub))

	return privateKeyPEM, authorizedKey, nil
}
