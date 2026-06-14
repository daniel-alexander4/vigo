// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// compliance docs — the CLI path for managing operator-uploaded compliance
// evidence artifacts. The web UI shows these read-only (observability only);
// every write (upload/replace/delete/link) goes through here, hitting the same
// `/api/v1/compliance/docs*` REST endpoints. Requires the local secrets backend
// (the docs are envelope-encrypted under the master key).

type docMeta struct {
	ID         string `json:"id"`
	DocType    string `json:"doc_type"`
	Filename   string `json:"filename"`
	MimeType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
	UploadedBy string `json:"uploaded_by"`
	UploadedAt string `json:"uploaded_at"`
}

var complianceDocsCmd = &cobra.Command{
	Use:   "docs",
	Short: "Manage compliance evidence documents (upload, list, link, delete)",
	Long: `Operator-uploaded compliance evidence artifacts (policies, contracts, IR plans,
BAAs, …), envelope-encrypted at rest. The web UI displays these read-only; all
writes go through this command. Requires the local secrets backend.`,
}

var complianceDocsUploadCmd = &cobra.Command{
	Use:   "upload <file>",
	Short: "Upload a compliance evidence document",
	Long: `Upload a policy, contract, plan, or other artifact as compliance evidence.

Examples:
  vigocli compliance docs upload incident-response-plan.pdf --type "IR Plan"
  vigocli compliance docs upload baa.pdf --type BAA --link hipaa:164.308 --link soc2:CC1.1`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		docType, _ := cmd.Flags().GetString("type")
		links, _ := cmd.Flags().GetStringArray("link")

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		linksJSON, err := linksToJSON(links)
		if err != nil {
			return err
		}

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", filepath.Base(path))
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
		if docType != "" {
			_ = mw.WriteField("doc_type", docType)
		}
		if linksJSON != "" {
			_ = mw.WriteField("links", linksJSON)
		}
		if err := mw.Close(); err != nil {
			return err
		}

		var out docMeta
		if err := newAPIClient().postRaw("/api/v1/compliance/docs", &buf,
			map[string]string{"Content-Type": mw.FormDataContentType()}, &out); err != nil {
			return err
		}
		fmt.Printf("%s uploaded %s (id %s, %d bytes)\n", green("OK"), out.Filename, out.ID, out.SizeBytes)
		return nil
	},
}

var complianceDocsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List compliance evidence documents",
	Long: `List uploaded compliance evidence documents.

Examples:
  vigocli compliance docs list
  vigocli compliance docs list --framework hipaa --control 164.308`,
	RunE: func(cmd *cobra.Command, args []string) error {
		framework, _ := cmd.Flags().GetString("framework")
		control, _ := cmd.Flags().GetString("control")
		path := "/api/v1/compliance/docs"
		if framework != "" && control != "" {
			path += fmt.Sprintf("?framework=%s&control_id=%s", framework, control)
		}
		var out struct {
			Docs []docMeta `json:"docs"`
		}
		if err := newAPIClient().get(path, &out); err != nil {
			return err
		}
		if len(out.Docs) == 0 {
			fmt.Println("No compliance documents.")
			return nil
		}
		fmt.Printf("%-22s  %-30s  %-12s  %10s  %s\n", "ID", "FILENAME", "TYPE", "SIZE", "UPLOADED BY")
		for _, d := range out.Docs {
			fmt.Printf("%-22s  %-30s  %-12s  %10d  %s\n", d.ID, truncate(d.Filename, 30), d.DocType, d.SizeBytes, d.UploadedBy)
		}
		return nil
	},
}

var complianceDocsDownloadCmd = &cobra.Command{
	Use:   "download <id>",
	Short: "Download a compliance evidence document",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		output, _ := cmd.Flags().GetString("output")
		resp, err := newAPIClient().getRaw("/api/v1/compliance/docs/" + id + "/download")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if output == "" {
			// Default to the server-provided filename. The filename comes
			// from a server header — strip any directory components so a
			// malicious/buggy Content-Disposition can't write outside cwd.
			if cd := resp.Header.Get("Content-Disposition"); strings.Contains(cd, "filename=") {
				output = filepath.Base(strings.Trim(strings.SplitN(cd, "filename=", 2)[1], `"`))
			}
			if output == "" || output == "." || output == "/" {
				output = id
			}
		}
		f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(f, resp.Body); err != nil {
			return err
		}
		fmt.Printf("%s downloaded to %s\n", green("OK"), output)
		return nil
	},
}

var complianceDocsDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a compliance evidence document (soft delete; --purge to remove permanently)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		purge, _ := cmd.Flags().GetBool("purge")
		client := newAPIClient()
		if purge {
			if err := client.post("/api/v1/compliance/docs/"+id+"/purge", nil, nil); err != nil {
				return err
			}
			fmt.Printf("%s purged %s\n", green("OK"), id)
			return nil
		}
		if err := client.delete("/api/v1/compliance/docs/"+id, nil); err != nil {
			return err
		}
		fmt.Printf("%s deleted %s\n", green("OK"), id)
		return nil
	},
}

var complianceDocsLinkCmd = &cobra.Command{
	Use:   "link <id> <framework> <control>",
	Short: "Link a document to a framework control",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]string{"framework": args[1], "control_id": args[2]}
		if err := newAPIClient().post("/api/v1/compliance/docs/"+args[0]+"/links", body, nil); err != nil {
			return err
		}
		fmt.Printf("%s linked %s to %s/%s\n", green("OK"), args[0], args[1], args[2])
		return nil
	},
}

var complianceDocsUnlinkCmd = &cobra.Command{
	Use:   "unlink <id> <framework> <control>",
	Short: "Remove a document's link to a framework control",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := newAPIClient().delete("/api/v1/compliance/docs/"+args[0]+"/links/"+args[1]+"/"+args[2], nil); err != nil {
			return err
		}
		fmt.Printf("%s unlinked %s from %s/%s\n", green("OK"), args[0], args[1], args[2])
		return nil
	},
}

// linksToJSON converts `framework:control` flag values into the JSON array the
// upload endpoint expects: [{"framework":..,"control_id":..}].
func linksToJSON(links []string) (string, error) {
	if len(links) == 0 {
		return "", nil
	}
	type link struct {
		Framework string `json:"framework"`
		ControlID string `json:"control_id"`
	}
	out := make([]link, 0, len(links))
	for _, l := range links {
		fw, ctrl, ok := strings.Cut(l, ":")
		if !ok || fw == "" || ctrl == "" {
			return "", fmt.Errorf("--link must be framework:control, got %q", l)
		}
		out = append(out, link{Framework: fw, ControlID: ctrl})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func init() {
	complianceDocsUploadCmd.Flags().String("type", "", "Document category (e.g. BAA, IR Plan)")
	complianceDocsUploadCmd.Flags().StringArray("link", nil, "Link to a control as framework:control (repeatable)")
	complianceDocsListCmd.Flags().String("framework", "", "Filter by framework (requires --control)")
	complianceDocsListCmd.Flags().String("control", "", "Filter by control ID (requires --framework)")
	complianceDocsDownloadCmd.Flags().StringP("output", "o", "", "Write to this path (default: server filename)")
	complianceDocsDeleteCmd.Flags().Bool("purge", false, "Permanently remove instead of soft delete")

	complianceDocsCmd.AddCommand(
		complianceDocsUploadCmd,
		complianceDocsListCmd,
		complianceDocsDownloadCmd,
		complianceDocsDeleteCmd,
		complianceDocsLinkCmd,
		complianceDocsUnlinkCmd,
	)
	complianceCmd.AddCommand(complianceDocsCmd)
}
