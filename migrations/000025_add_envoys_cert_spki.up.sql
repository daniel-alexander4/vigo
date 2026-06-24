-- Cert-pinning (security): store the sha256 of each envoy's enrolled mTLS
-- client-cert SubjectPublicKeyInfo, so the check-in verify path can reject a
-- TLS-terminating proxy / MITM presenting a different CA-signed cert. Empty
-- string = grandfathered (enrolled before pinning); enforced after the
-- envoy's next (re)enrollment, which captures the SPKI.
ALTER TABLE envoys ADD COLUMN cert_spki_sha256 TEXT NOT NULL DEFAULT '';
