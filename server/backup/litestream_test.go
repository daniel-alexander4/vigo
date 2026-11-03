// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"strings"
	"testing"
)

func TestParseReplicaURL_S3(t *testing.T) {
	rt, bucket, path, err := parseReplicaURL("s3://my-bucket/vigo/backups")
	if err != nil {
		t.Fatal(err)
	}
	if rt != "s3" {
		t.Errorf("type = %q, want s3", rt)
	}
	if bucket != "my-bucket" {
		t.Errorf("bucket = %q, want my-bucket", bucket)
	}
	if path != "vigo/backups" {
		t.Errorf("path = %q, want vigo/backups", path)
	}
}

func TestParseReplicaURL_GCS(t *testing.T) {
	rt, bucket, path, err := parseReplicaURL("gcs://my-gcs-bucket/db-backup")
	if err != nil {
		t.Fatal(err)
	}
	if rt != "gcs" {
		t.Errorf("type = %q, want gcs", rt)
	}
	if bucket != "my-gcs-bucket" {
		t.Errorf("bucket = %q, want my-gcs-bucket", bucket)
	}
	if path != "db-backup" {
		t.Errorf("path = %q, want db-backup", path)
	}
}

func TestParseReplicaURL_SFTP(t *testing.T) {
	rt, bucket, path, err := parseReplicaURL("sftp://backup.example.com/srv/vigo")
	if err != nil {
		t.Fatal(err)
	}
	if rt != "sftp" {
		t.Errorf("type = %q, want sftp", rt)
	}
	if bucket != "backup.example.com" {
		t.Errorf("host = %q, want backup.example.com", bucket)
	}
	if path != "srv/vigo" {
		t.Errorf("path = %q, want srv/vigo", path)
	}
}

func TestParseReplicaURL_File(t *testing.T) {
	rt, bucket, path, err := parseReplicaURL("file:///mnt/backup/vigo")
	if err != nil {
		t.Fatal(err)
	}
	if rt != "file" {
		t.Errorf("type = %q, want file", rt)
	}
	if bucket != "/mnt/backup/vigo" {
		t.Errorf("path = %q, want /mnt/backup/vigo", bucket)
	}
	if path != "" {
		t.Errorf("extra path = %q, want empty", path)
	}
}

func TestParseReplicaURL_BadScheme(t *testing.T) {
	_, _, _, err := parseReplicaURL("ftp://host/path")
	if err == nil {
		t.Fatal("expected error for ftp:// scheme")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error = %q, want 'unsupported'", err)
	}
}

func TestParseReplicaURL_NoScheme(t *testing.T) {
	_, _, _, err := parseReplicaURL("/local/path")
	if err == nil {
		t.Fatal("expected error for missing scheme")
	}
	if !strings.Contains(err.Error(), "missing scheme") {
		t.Errorf("error = %q, want 'missing scheme'", err)
	}
}

func TestGenerateConfig_S3(t *testing.T) {
	cfg := Config{
		URL:             "s3://my-bucket/vigo",
		DBPath:          "/srv/vigo/db/vigo.db",
		AccessKeyID:     "AKID",
		SecretAccessKey: "SECRET",
		Region:          "us-west-2",
		Retention:        "168h",
		SyncInterval:    "5s",
	}
	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"path: /srv/vigo/db/vigo.db",
		"type: s3",
		"bucket: my-bucket",
		"path: vigo",
		"region: us-west-2",
		"retention: 168h",
		"sync-interval: 5s",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q\n---\n%s", want, content)
		}
	}
}

func TestGenerateConfig_S3_MinIO(t *testing.T) {
	cfg := Config{
		URL:            "s3://backup/vigo",
		DBPath:         "/data/vigo.db",
		Endpoint:       "http://minio:9000",
		ForcePathStyle: true,
	}
	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"endpoint: http://minio:9000",
		"force-path-style: true",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q\n---\n%s", want, content)
		}
	}
}

func TestGenerateConfig_File(t *testing.T) {
	cfg := Config{
		URL:    "file:///mnt/backup/vigo",
		DBPath: "/data/vigo.db",
	}
	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"type: file",
		"path: /mnt/backup/vigo",
		"retention: 720h",
		"sync-interval: 1s",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config missing %q\n---\n%s", want, content)
		}
	}
}

func TestGenerateConfig_Defaults(t *testing.T) {
	cfg := Config{
		URL:    "s3://bucket/path",
		DBPath: "/db/test.db",
	}
	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Verify defaults are applied.
	if !strings.Contains(content, "retention: 720h") {
		t.Errorf("expected default retention 720h\n---\n%s", content)
	}
	if !strings.Contains(content, "sync-interval: 1s") {
		t.Errorf("expected default sync-interval 1s\n---\n%s", content)
	}
	// Region should not appear when empty.
	if strings.Contains(content, "region:") {
		t.Errorf("region should not appear when empty\n---\n%s", content)
	}
}

func TestNewReplicator(t *testing.T) {
	r := NewReplicator(Config{URL: "s3://bucket/path", DBPath: "/db/test.db"})
	if r == nil {
		t.Fatal("NewReplicator returned nil")
	}
	if r.cfg.URL != "s3://bucket/path" {
		t.Errorf("URL = %q, want s3://bucket/path", r.cfg.URL)
	}
}
