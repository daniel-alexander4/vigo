// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package sandgorgon

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
)

// ImportResult tracks outcomes of a bulk import.
type ImportResult struct {
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors,omitempty"`
}

// ImportCSV reads a CSV file and creates/updates sandgorgon assets.
//
// Expected columns: hostname, bmc_address, serial_number, asset_tag, rack_location
// Optional columns: bmc_username, bmc_password, model
//
// If hostname matches an enrolled envoy, the asset is linked to that envoy.
// Otherwise, the asset is created in "available" state.
func (m *Manager) ImportCSV(ctx context.Context, path string, fleetIndex *fleet.FleetIndex) (*ImportResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening CSV: %w", err)
	}
	defer f.Close()

	return m.ImportCSVReader(ctx, f, fleetIndex)
}

// ImportCSVReader reads from an io.Reader (for API uploads).
func (m *Manager) ImportCSVReader(ctx context.Context, r io.Reader, fleetIndex *fleet.FleetIndex) (*ImportResult, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true

	// Read header row.
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}

	// Map column names to indices.
	colIdx := make(map[string]int)
	for i, col := range header {
		colIdx[strings.TrimSpace(strings.ToLower(col))] = i
	}

	// Validate required columns.
	if _, ok := colIdx["bmc_address"]; !ok {
		return nil, fmt.Errorf("CSV missing required column: bmc_address")
	}

	result := &ImportResult{}
	rowNum := 1 // 1 = header already consumed

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("row %d: %v", rowNum+1, err))
			rowNum++
			continue
		}
		rowNum++

		bmcAddr := getCol(row, colIdx, "bmc_address")
		if bmcAddr == "" {
			result.Errors = append(result.Errors, fmt.Sprintf("row %d: empty bmc_address", rowNum))
			result.Skipped++
			continue
		}

		hostname := getCol(row, colIdx, "hostname")

		// Check if asset already exists by BMC address.
		existing, err := m.queries.GetSandgorgonAssetByBMC(ctx, bmcAddr)
		if err == nil {
			// Update existing — merge non-empty fields.
			slog.Debug("CSV import: asset exists, skipping", "bmc", bmcAddr, "hostname", existing.Hostname)
			result.Skipped++
			continue
		}

		// Resolve envoy ID from hostname.
		var envoyID sql.NullString
		if hostname != "" && fleetIndex != nil {
			if eid := fleetIndex.GetByHostname(hostname); eid != "" {
				envoyID = sql.NullString{String: eid, Valid: true}
			}
		}

		// Determine lifecycle state.
		state := "available"
		if envoyID.Valid {
			state = "commissioned"
		}

		_, err = m.queries.CreateSandgorgonAsset(ctx, db.CreateSandgorgonAssetParams{
			Hostname:    hostname,
			BmcAddress:  bmcAddr,
			BmcUsername: getCol(row, colIdx, "bmc_username"),
			// SECURITY (deferred): BMC password is stored PLAINTEXT in the DB.
			// Before sandgorgon is un-deferred (sandgorgon.enabled), wrap this
			// via the secrets provider — store a secret: ref, not the value.
			// Tracked separately; do NOT attempt the schema migration here.
			BmcPassword:    getCol(row, colIdx, "bmc_password"),
			SerialNumber:   getCol(row, colIdx, "serial_number"),
			Manufacturer:   getCol(row, colIdx, "manufacturer"),
			Model:          getCol(row, colIdx, "model"),
			AssetTag:       getCol(row, colIdx, "asset_tag"),
			RackLocation:   getCol(row, colIdx, "rack_location"),
			LifecycleState: state,
			EnvoyID:        envoyID,
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("row %d (%s): %v", rowNum, hostname, err))
			continue
		}

		result.Created++
		slog.Info("CSV import: created asset", "hostname", hostname, "bmc", bmcAddr, "state", state)
	}

	return result, nil
}

func getCol(row []string, colIdx map[string]int, name string) string {
	if idx, ok := colIdx[name]; ok && idx < len(row) {
		return strings.TrimSpace(row[idx])
	}
	return ""
}
