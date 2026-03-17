// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var askThrall string

var askCmd = &cobra.Command{
	Use:   "ask [question]",
	Short: "Ask the AI assistant a natural language question about fleet state, configs, or compliance",
	Long: `Ask the Hobnob AI assistant a natural language question about your
infrastructure. The assistant can query thralls, runs, traits, compliance,
configs, modules, and packages to answer questions.

If no question argument is given, reads from stdin (useful for piping config
files for review).

Examples:
  hobnobcli ask "how many thralls do I have?"
  hobnobcli ask --thrall web-03 "why is it failing?"
  hobnobcli ask "write a module for nginx with TLS"
  hobnobcli ask < module.hnb`,
	RunE: runAsk,
}

func init() {
	askCmd.Flags().StringVar(&askThrall, "thrall", "", "Thrall hostname context for the question")
}

type askRequest struct {
	Question string     `json:"question"`
	Context  askContext `json:"context,omitempty"`
}

type askContext struct {
	Thrall string `json:"thrall,omitempty"`
}

type askResponse struct {
	Answer     string   `json:"answer"`
	ToolsUsed  []string `json:"tools_used"`
	TokensUsed int      `json:"tokens_used"`
}

type sseEvent struct {
	Type        string   `json:"type"`
	Text        string   `json:"text,omitempty"`
	Status      string   `json:"status,omitempty"`
	ToolsCalled []string `json:"tools_called,omitempty"`
	ToolsUsed   []string `json:"tools_used,omitempty"`
	TokensUsed  int      `json:"tokens_used,omitempty"`
}

func runAsk(cmd *cobra.Command, args []string) error {
	var question string
	if len(args) > 0 {
		question = strings.Join(args, " ")
	} else {
		// Read from stdin.
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		if len(data) == 0 {
			return fmt.Errorf("no question provided (pass as argument or pipe via stdin)")
		}
		question = "Review this configuration:\n\n" + string(data)
	}

	req := askRequest{
		Question: question,
		Context:  askContext{Thrall: askThrall},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", serverURL+"/api/v1/ask", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 5 * time.Minute}
	if isLocalhost(serverURL) {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}

	// If response is SSE, stream it.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return streamSSE(resp.Body)
	}

	// Otherwise, JSON response.
	var result askResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	fmt.Println(result.Answer)
	if len(result.ToolsUsed) > 0 {
		fmt.Fprintf(os.Stderr, "\n[tools: %s | tokens: %d]\n", strings.Join(result.ToolsUsed, ", "), result.TokensUsed)
	}
	return nil
}

func streamSSE(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	var toolsUsed []string
	var tokensUsed int

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var event sseEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "status":
				if len(event.ToolsCalled) > 0 {
					fmt.Fprintf(os.Stderr, "[querying: %s]\n", strings.Join(event.ToolsCalled, ", "))
				}
			case "delta":
				fmt.Print(event.Text)
			case "done":
				toolsUsed = event.ToolsUsed
				tokensUsed = event.TokensUsed
			case "error":
				return fmt.Errorf("AI error: %s", event.Text)
			}
		}
	}

	fmt.Println()
	if len(toolsUsed) > 0 {
		fmt.Fprintf(os.Stderr, "\n[tools: %s | tokens: %d]\n", strings.Join(toolsUsed, ", "), tokensUsed)
	}

	return scanner.Err()
}
