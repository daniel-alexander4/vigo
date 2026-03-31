// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateEphemeralSSHKey creates an RSA keypair for a single Scrier session.
// RSA is used because guacd's libssh2 does not reliably support ED25519 keys
// in OpenSSH PEM format. Returns the PEM-encoded private key (for guacd) and
// the OpenSSH authorized_keys format public key (for the agent to install).
func GenerateEphemeralSSHKey() (privateKeyPEM []byte, authorizedKey string, err error) {
	priv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, "", fmt.Errorf("generate rsa key: %w", err)
	}

	// PKCS#1 PEM — universally supported by guacd/libssh2.
	privateKeyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	// OpenSSH authorized_keys format for the agent.
	sshPub, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, "", fmt.Errorf("convert to ssh public key: %w", err)
	}
	authorizedKey = string(ssh.MarshalAuthorizedKey(sshPub))

	return privateKeyPEM, authorizedKey, nil
}
