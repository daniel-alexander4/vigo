// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package sandgorgon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"

	"github.com/daniel-alexander4/vigo/server/db"
)

// DefaultImagesDir is the default location for OS installation images.
const DefaultImagesDir = "/srv/vigo/sandgorgon/images"

// ImageRegistration contains parameters for registering an OS image.
type ImageRegistration struct {
	Name        string // human-readable name, e.g. "ubuntu-24.04-amd64"
	OSFamily    string // debian, rhel, windows
	OSVersion   string // 24.04, 9.4, 2022
	Arch        string // amd64, arm64
	ImageType   string // iso, kernel, tarball
	Path        string // absolute path to the image file
	KernelPath  string // for kernel type: path to vmlinuz
	InitrdPath  string // for kernel type: path to initrd
	BootParams  string // extra kernel boot parameters
	PreseedType string // preseed, kickstart, autoinstall, autounattend
}

// RegisterImage validates and registers an OS image in the database.
func (m *Manager) RegisterImage(ctx context.Context, reg ImageRegistration) (db.SandgorgonImage, error) {
	if reg.Name == "" {
		return db.SandgorgonImage{}, fmt.Errorf("image name is required")
	}
	if reg.Path == "" {
		return db.SandgorgonImage{}, fmt.Errorf("image path is required")
	}

	// Validate file exists.
	info, err := os.Stat(reg.Path)
	if err != nil {
		return db.SandgorgonImage{}, fmt.Errorf("image file not found: %w", err)
	}

	// Compute SHA256 checksum.
	checksum, err := fileChecksum(reg.Path)
	if err != nil {
		return db.SandgorgonImage{}, fmt.Errorf("computing checksum: %w", err)
	}

	// Validate kernel/initrd for kernel type.
	if reg.ImageType == "kernel" {
		if reg.KernelPath == "" || reg.InitrdPath == "" {
			return db.SandgorgonImage{}, fmt.Errorf("kernel_path and initrd_path required for kernel image type")
		}
		if _, err := os.Stat(reg.KernelPath); err != nil {
			return db.SandgorgonImage{}, fmt.Errorf("kernel not found: %w", err)
		}
		if _, err := os.Stat(reg.InitrdPath); err != nil {
			return db.SandgorgonImage{}, fmt.Errorf("initrd not found: %w", err)
		}
	}

	// Default preseed type from OS family.
	if reg.PreseedType == "" {
		reg.PreseedType = defaultPreseedType(reg.OSFamily)
	}
	if reg.Arch == "" {
		reg.Arch = "amd64"
	}
	if reg.ImageType == "" {
		reg.ImageType = "iso"
	}

	return m.queries.CreateSandgorgonImage(ctx, db.CreateSandgorgonImageParams{
		Name:        reg.Name,
		OsFamily:    reg.OSFamily,
		OsVersion:   reg.OSVersion,
		Arch:        reg.Arch,
		ImageType:   reg.ImageType,
		Path:        reg.Path,
		KernelPath:  reg.KernelPath,
		InitrdPath:  reg.InitrdPath,
		BootParams:  reg.BootParams,
		PreseedType: reg.PreseedType,
		Sha256:      checksum,
		SizeBytes:   info.Size(),
	})
}

// RemoveImage deletes an image registration (does not delete the file).
func (m *Manager) RemoveImage(ctx context.Context, imageID string) error {
	return m.queries.DeleteSandgorgonImage(ctx, imageID)
}

func defaultPreseedType(osFamily string) string {
	switch osFamily {
	case "debian":
		return "preseed"
	case "ubuntu":
		return "autoinstall"
	case "rhel", "rocky", "centos", "fedora", "alma":
		return "kickstart"
	case "windows":
		return "autounattend"
	default:
		return ""
	}
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
