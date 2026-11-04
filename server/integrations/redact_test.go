// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package integrations

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestPostJSON_ErrorOmitsWebhookURL: for Slack and Teams the webhook URL
// is the credential, and Dispatch logs postJSON's error — so a transport
// or parse failure must not carry the URL.
func TestPostJSON_ErrorOmitsWebhookURL(t *testing.T) {
	const token = "XXXXsecretTOKENXXXX"
	client := &http.Client{Timeout: 2 * time.Second}
	for _, u := range []string{
		"http://127.0.0.1:1/services/T000/B000/" + token, // connection refused
		"http://[::1/services/" + token,                  // unparsable
	} {
		err := postJSON(context.Background(), client, u, nil, map[string]string{"text": "x"})
		if err == nil {
			t.Fatalf("postJSON(%q) succeeded", u)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("error leaks the webhook URL: %v", err)
		}
	}
}
