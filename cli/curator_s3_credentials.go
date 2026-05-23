// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// `vigocli swarm curator s3-credentials` — manage the AWS-SigV4 credentials
// that authenticate non-envoy clients against the curator S3-compatible
// object API (ADR-027, `swarm.curator.s3_port`). Admin-only; the backing
// store requires the `local` secrets backend.

var curatorS3CredsCmd = &cobra.Command{
	Use:   "s3-credentials",
	Short: "Manage SigV4 credentials for the curator S3 API (ADR-027)",
	Long: `Mint, list, and revoke the AWS-SigV4 credentials that authenticate
non-envoy clients (CI runners, dev laptops, any S3 SDK) against the
curator S3-compatible object API exposed on 'swarm.curator.s3_port'.

The secret access key is derived, never stored — it is shown once at
create time and cannot be recovered. The server requires the 'local'
secrets backend for this command.`,
}

var curatorS3CredsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Mint a new S3 credential (the secret is shown once)",
	Args:  cobra.NoArgs,
	RunE:  runCuratorS3CredCreate,
}

var curatorS3CredsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List S3 credentials (metadata only — no secrets)",
	Args:  cobra.NoArgs,
	RunE:  runCuratorS3CredList,
}

var curatorS3CredsRevokeCmd = &cobra.Command{
	Use:   "revoke <access-key-id>",
	Short: "Revoke an S3 credential",
	Args:  cobra.ExactArgs(1),
	RunE:  runCuratorS3CredRevoke,
}

func runCuratorS3CredCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	scope, _ := cmd.Flags().GetString("scope")
	asJSON, _ := cmd.Flags().GetBool("json")

	body := map[string]string{"name": name, "scope": scope}
	var resp struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		Scope           string `json:"scope"`
		Name            string `json:"name"`
	}
	if err := newAPIClient().post("/api/v1/swarm/curator/s3-credentials", body, &resp); err != nil {
		return fmt.Errorf("creating s3 credential: %w", err)
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	fmt.Println("Curator S3 credential created.")
	fmt.Printf("  Access key ID:     %s\n", resp.AccessKeyID)
	fmt.Printf("  Secret access key: %s\n", resp.SecretAccessKey)
	fmt.Printf("  Scope:             %s\n", resp.Scope)
	if resp.Name != "" {
		fmt.Printf("  Name:              %s\n", resp.Name)
	}
	fmt.Println()
	fmt.Println(yellow("The secret access key is shown once and cannot be recovered — store it now."))
	return nil
}

func runCuratorS3CredList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	var resp []struct {
		AccessKeyID string    `json:"access_key_id"`
		Scope       string    `json:"scope"`
		Name        string    `json:"name"`
		CreatedAt   time.Time `json:"created_at"`
	}
	if err := newAPIClient().get("/api/v1/swarm/curator/s3-credentials", &resp); err != nil {
		return fmt.Errorf("listing s3 credentials: %w", err)
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	if len(resp) == 0 {
		fmt.Println("No curator S3 credentials.")
		return nil
	}
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", bold("ACCESS KEY ID"), bold("SCOPE"), bold("NAME"), bold("CREATED"))
	for _, c := range resp {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.AccessKeyID, c.Scope, c.Name,
			c.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	}
	w.Flush()
	return nil
}

func runCuratorS3CredRevoke(_ *cobra.Command, args []string) error {
	if err := newAPIClient().delete("/api/v1/swarm/curator/s3-credentials/"+url.PathEscape(args[0]), nil); err != nil {
		return fmt.Errorf("revoking s3 credential: %w", err)
	}
	fmt.Printf("Revoked S3 credential %s.\n", args[0])
	return nil
}

func init() {
	curatorS3CredsCreateCmd.Flags().String("name", "", "Descriptive label for the credential")
	curatorS3CredsCreateCmd.Flags().String("scope", "*", "Publisher prefix the credential may read (`*` = all publishers)")
	curatorS3CredsCreateCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of text")
	curatorS3CredsListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	curatorS3CredsCmd.AddCommand(curatorS3CredsCreateCmd, curatorS3CredsListCmd, curatorS3CredsRevokeCmd)
	curatorCmd.AddCommand(curatorS3CredsCmd)
}
