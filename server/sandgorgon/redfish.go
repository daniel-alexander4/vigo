// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package sandgorgon implements bare metal lifecycle management:
// BMC discovery, decommissioning, disk sanitization, and NIST 800-88
// certificate generation.
package sandgorgon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RedfishClient communicates with a BMC via the Redfish REST API.
type RedfishClient struct {
	address  string
	username string
	password string
	client   *http.Client
}

// NewRedfishClient creates a client for the given BMC address.
// TLS verification is skipped because BMC certificates are typically self-signed.
func NewRedfishClient(address, username, password string) *RedfishClient {
	if !strings.HasPrefix(address, "https://") && !strings.HasPrefix(address, "http://") {
		address = "https://" + address
	}
	return &RedfishClient{
		address:  strings.TrimRight(address, "/"),
		username: username,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// SystemInfo contains hardware identity from the BMC.
type SystemInfo struct {
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
	SerialNumber string `json:"SerialNumber"`
	SKU          string `json:"SKU"`
	BIOSVersion  string `json:"BiosVersion"`
	PowerState   string `json:"PowerState"`
	UUID         string `json:"UUID"`
}

// StorageDrive describes a single storage device.
type StorageDrive struct {
	Name         string `json:"Name"`
	MediaType    string `json:"MediaType"` // SSD, HDD, NVMe
	Protocol     string `json:"Protocol"`  // NVMe, SATA, SAS
	CapacityGB   int64  `json:"CapacityBytes"`
	SerialNumber string `json:"SerialNumber"`
	Model        string `json:"Model"`
	// Sanitization capabilities discovered from Redfish.
	SupportsSecureErase bool `json:"SupportsSecureErase"`
	SupportsCryptoErase bool `json:"SupportsCryptoErase"`
}

// SanitizeResult captures the outcome of a disk sanitization operation.
type SanitizeResult struct {
	DriveName              string    `json:"drive_name"`
	DriveSerial            string    `json:"drive_serial"`
	MediaType              string    `json:"media_type"`
	Method                 string    `json:"method"` // Clear, Purge, Destroy
	VerificationMethod     string    `json:"verification_method"`
	VerificationResult     string    `json:"verification_result"` // verified, unverifiable, failed
	VerificationLimitations string   `json:"verification_limitations"`
	StartedAt              time.Time `json:"started_at"`
	CompletedAt            time.Time `json:"completed_at"`
}

// get performs an authenticated GET against the Redfish API.
func (c *RedfishClient) get(ctx context.Context, path string) ([]byte, error) {
	url := c.address + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("redfish GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("redfish GET %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// post performs an authenticated POST against the Redfish API.
func (c *RedfishClient) post(ctx context.Context, path string, payload any) ([]byte, error) {
	url := c.address + path
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(string(data))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("redfish POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("redfish POST %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// Ping checks BMC reachability.
func (c *RedfishClient) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "/redfish/v1/")
	return err
}

// GetSystemInfo retrieves hardware identity from the first system resource.
func (c *RedfishClient) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	// Discover systems collection.
	body, err := c.get(ctx, "/redfish/v1/Systems")
	if err != nil {
		return nil, err
	}
	var coll struct {
		Members []struct {
			ODataID string `json:"@odata.id"`
		} `json:"Members"`
	}
	if err := json.Unmarshal(body, &coll); err != nil {
		return nil, fmt.Errorf("parsing systems collection: %w", err)
	}
	if len(coll.Members) == 0 {
		return nil, fmt.Errorf("no systems found via Redfish")
	}

	// Get first system.
	sysBody, err := c.get(ctx, coll.Members[0].ODataID)
	if err != nil {
		return nil, err
	}
	var info SystemInfo
	if err := json.Unmarshal(sysBody, &info); err != nil {
		return nil, fmt.Errorf("parsing system info: %w", err)
	}
	return &info, nil
}

// ListDrives enumerates storage drives via Redfish Storage subsystem.
func (c *RedfishClient) ListDrives(ctx context.Context) ([]StorageDrive, error) {
	// Try /redfish/v1/Systems/1/Storage first.
	body, err := c.get(ctx, "/redfish/v1/Systems/1/Storage")
	if err != nil {
		return nil, err
	}

	var coll struct {
		Members []struct {
			ODataID string `json:"@odata.id"`
		} `json:"Members"`
	}
	if err := json.Unmarshal(body, &coll); err != nil {
		return nil, fmt.Errorf("parsing storage collection: %w", err)
	}

	var drives []StorageDrive
	for _, member := range coll.Members {
		ctrlBody, err := c.get(ctx, member.ODataID)
		if err != nil {
			continue
		}
		var ctrl struct {
			Drives []struct {
				ODataID string `json:"@odata.id"`
			} `json:"Drives"`
		}
		if err := json.Unmarshal(ctrlBody, &ctrl); err != nil {
			continue
		}

		for _, driveRef := range ctrl.Drives {
			driveBody, err := c.get(ctx, driveRef.ODataID)
			if err != nil {
				continue
			}
			var d struct {
				Name          string `json:"Name"`
				MediaType     string `json:"MediaType"`
				Protocol      string `json:"Protocol"`
				CapacityBytes int64  `json:"CapacityBytes"`
				SerialNumber  string `json:"SerialNumber"`
				Model         string `json:"Model"`
				Operations    []struct {
					OperationName string `json:"OperationName"`
				} `json:"Operations"`
			}
			if err := json.Unmarshal(driveBody, &d); err != nil {
				continue
			}

			drive := StorageDrive{
				Name:         d.Name,
				MediaType:    d.MediaType,
				Protocol:     d.Protocol,
				CapacityGB:   d.CapacityBytes / (1024 * 1024 * 1024),
				SerialNumber: d.SerialNumber,
				Model:        d.Model,
			}

			// Check for sanitization capabilities.
			for _, op := range d.Operations {
				switch op.OperationName {
				case "SecureErase":
					drive.SupportsSecureErase = true
				case "CryptographicErasePD":
					drive.SupportsCryptoErase = true
				}
			}
			drives = append(drives, drive)
		}
	}
	return drives, nil
}

// SecureEraseDrive dispatches a Redfish SecureErase action on a drive.
func (c *RedfishClient) SecureEraseDrive(ctx context.Context, drivePath string) error {
	_, err := c.post(ctx, drivePath+"/Actions/Drive.SecureErase", nil)
	return err
}

// SetBootOverridePXE sets one-time PXE boot on the first system.
func (c *RedfishClient) SetBootOverridePXE(ctx context.Context) error {
	payload := map[string]any{
		"Boot": map[string]string{
			"BootSourceOverrideTarget":  "Pxe",
			"BootSourceOverrideEnabled": "Once",
		},
	}
	_, err := c.patch(ctx, "/redfish/v1/Systems/1", payload)
	return err
}

// PowerCycle performs a graceful restart (ForceRestart if already on, ForceOn if off).
func (c *RedfishClient) PowerCycle(ctx context.Context) error {
	// Check current power state.
	info, err := c.GetSystemInfo(ctx)
	if err != nil {
		return fmt.Errorf("checking power state: %w", err)
	}

	resetType := "ForceRestart"
	if info.PowerState == "Off" {
		resetType = "On"
	}

	payload := map[string]string{"ResetType": resetType}
	_, err = c.post(ctx, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", payload)
	return err
}

// patch performs an authenticated PATCH against the Redfish API.
func (c *RedfishClient) patch(ctx context.Context, path string, payload any) ([]byte, error) {
	url := c.address + path
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("redfish PATCH %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("redfish PATCH %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
