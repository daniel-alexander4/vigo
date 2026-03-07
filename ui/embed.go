// Package ui provides embedded web UI assets and rendering for the Isogov dashboard.
package ui

import "embed"

//go:embed templates/* static/*
var Assets embed.FS
