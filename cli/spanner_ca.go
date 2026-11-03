// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"git.alexander4.io/vigo/server/secrets"
)

// spannerCaCmd manages the SHARED fleet CA across spanner bolts. ADR-026
// config-driven peering (option 3): the CA is established out-of-band and NEVER
// crosses the wire. The founder exports its CA; each joining bolt imports it so
// every bolt signs/trusts certs under one CA — the prerequisite that makes
// cross-bolt mTLS (enrollment routing, peer admin) work. With the CA off the
// wire, a rogue bolt that slips a weak admission gate is bounded (can't read
// fleet data, receive routed envoys, or mint trusted certs).
var spannerCaCmd = &cobra.Command{
	Use:   "ca",
	Short: "Manage the shared fleet CA across spanner bolts",
	Long: `Establish ONE fleet CA across every bolt in a spanner — the prerequisite
for cross-bolt mTLS (enrollment routing + peer admin). The CA private key is
never sent over the network: export it on the founding bolt, transfer the file
out-of-band, and import it on each joining bolt before it joins.`,
}

var spannerCaExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export this bolt's fleet CA (cert + key) for other bolts to adopt",
	Long: `Writes this server's CA certificate AND private key as a single PEM
bundle. Run on the FOUNDING bolt. The bundle contains the fleet's crown-jewel
private key — keep the file 0600 and transfer it over a secure channel (scp,
not email). Each other bolt adopts it via 'vigocli spanner ca import'.`,
	RunE: runSpannerCaExport,
}

var spannerCaImportCmd = &cobra.Command{
	Use:   "import --from <bundle>",
	Short: "Adopt a fleet CA exported from the founder (re-signs this bolt's server cert)",
	Long: `Installs the shared fleet CA (from 'spanner ca export') as this bolt's CA
and re-signs this bolt's server certificate from it, with this host's own SANs.
Run on each JOINING bolt before it joins. Restart vigosrv afterward to load it.`,
	RunE: runSpannerCaImport,
}

func init() {
	spannerCaCmd.PersistentFlags().String("tls-dir", "/srv/vigo/tls", "Directory holding this server's TLS material (ca, ca_key, cert, key)")
	spannerCaExportCmd.Flags().String("out", "", "File to write the CA bundle to (0600); empty = stdout")
	spannerCaImportCmd.Flags().String("from", "", "CA bundle file produced by 'spanner ca export' (required)")
	spannerCaImportCmd.Flags().StringSlice("san", nil, "Extra SAN(s) to add to the re-signed server cert (repeatable)")
	spannerCaCmd.AddCommand(spannerCaExportCmd, spannerCaImportCmd)
	spannerCmd.AddCommand(spannerCaCmd)
}

func runSpannerCaExport(cmd *cobra.Command, _ []string) error {
	tlsDir, _ := cmd.Flags().GetString("tls-dir")
	caPEM, caKeyPEM, err := secrets.ExportCA(tlsDir)
	if err != nil {
		return fmt.Errorf("exporting fleet CA: %w", err)
	}
	bundle := append(append([]byte{}, caPEM...), caKeyPEM...)

	out, _ := cmd.Flags().GetString("out")
	if out == "" {
		fmt.Fprintln(os.Stderr, "WARNING: emitting the fleet CA PRIVATE KEY to stdout — redirect to a 0600 file and transfer it over a secure channel.")
		_, err := os.Stdout.Write(bundle)
		return err
	}
	if err := os.WriteFile(out, bundle, 0600); err != nil {
		return fmt.Errorf("writing CA bundle: %w", err)
	}
	fmt.Printf("Fleet CA exported to %s (0600).\n", out)
	fmt.Println("Transfer it securely to each joining bolt, then run: vigocli spanner ca import --from <file>")
	return nil
}

func runSpannerCaImport(cmd *cobra.Command, _ []string) error {
	from, _ := cmd.Flags().GetString("from")
	if from == "" {
		return fmt.Errorf("--from <bundle-file> is required (the file produced by 'spanner ca export')")
	}
	tlsDir, _ := cmd.Flags().GetString("tls-dir")
	sans, _ := cmd.Flags().GetStringSlice("san")

	bundle, err := os.ReadFile(from)
	if err != nil {
		return fmt.Errorf("reading CA bundle: %w", err)
	}
	caPEM, caKeyPEM, err := splitCABundle(bundle)
	if err != nil {
		return err
	}
	if err := secrets.AdoptCA(tlsDir, caPEM, caKeyPEM, sans...); err != nil {
		return fmt.Errorf("adopting fleet CA: %w", err)
	}
	fmt.Printf("Adopted the shared fleet CA into %s and re-signed this bolt's server cert.\n", tlsDir)
	fmt.Println("Restart vigosrv to load it (e.g. `docker restart vigo` or `systemctl restart vigosrv`).")
	return nil
}

// splitCABundle separates the concatenated CA cert + key PEM blocks produced by
// 'spanner ca export'. Order-independent; ignores any extra blocks.
func splitCABundle(bundle []byte) (caPEM, caKeyPEM []byte, err error) {
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE" && caPEM == nil:
			caPEM = pem.EncodeToMemory(block)
		case strings.Contains(block.Type, "PRIVATE KEY") && caKeyPEM == nil:
			caKeyPEM = pem.EncodeToMemory(block)
		}
	}
	if caPEM == nil || caKeyPEM == nil {
		return nil, nil, fmt.Errorf("bundle must contain both a CERTIFICATE and a PRIVATE KEY PEM block")
	}
	return caPEM, caKeyPEM, nil
}
