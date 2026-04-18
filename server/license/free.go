// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package license

// freeLicenseJSON is a pre-signed 100-node community license embedded in the
// binary. It is used when no .lic files are found on disk, giving every
// install a working free tier with zero manual steps.
//
// Signed with the same Ed25519 keypair whose public half lives in pubkey.go.
// Expires 2099-12-31 — effectively perpetual.
//
// Regenerate this blob with:
//   vigo-license-manager license free-license --max-envoys 100
const freeLicenseJSON = `{
  "payload": {
    "customer_name": "Community",
    "customer_id": "community",
    "license_id": "free-100",
    "tier": "free",
    "max_envoys": 100,
    "issued_at": "2026-03-12",
    "expires_at": "2099-12-31",
    "version": 1
  },
  "signature": "pFbtgqtQT/Q3bHj1JoRUYF+D7gRnQQadQZsI78gLkeffEJsLiClySB5ACKkHe5arOS7kigFP5idtgjlHsKBlBA=="
}`

// FreeLicense returns the embedded community license.
func FreeLicense() (*License, error) {
	lic, err := Parse([]byte(freeLicenseJSON))
	if err != nil {
		return nil, err
	}
	if err := lic.Verify(); err != nil {
		return nil, err
	}
	return lic, nil
}
