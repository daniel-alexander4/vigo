// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package license

// freeLicenseJSON is a pre-signed 25-node community license embedded in the
// binary. It is used when no .lic files are found on disk, giving every
// install a working free tier with zero manual steps.
//
// Signed with the same Ed25519 keypair whose public half lives in pubkey.go.
// Expires 2099-12-31 — effectively perpetual.
const freeLicenseJSON = `{
  "payload": {
    "customer_name": "Community",
    "customer_id": "community",
    "license_id": "free-25",
    "max_thralls": 25,
    "issued_at": "2026-03-12",
    "expires_at": "2099-12-31",
    "version": 1
  },
  "signature": "HctTBQ64obqwcW8RQgEqfQWoc7Agb/uXDUPTwq3DURSR23+gNe88BcTrvsDxQpOx29go2mBjSYJb1YxNkPARDA=="
}`

// FreeLicense returns the embedded 25-node community license.
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
