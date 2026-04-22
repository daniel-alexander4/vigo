// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package sandgorgon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/db"
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

	// For ISO images, auto-extract kernel/initrd if not provided.
	if reg.ImageType == "iso" && reg.KernelPath == "" && reg.InitrdPath == "" {
		kernel, initrd, extractErr := extractKernelFromISO(reg.Path, reg.Name)
		if extractErr != nil {
			return db.SandgorgonImage{}, fmt.Errorf("extracting kernel/initrd from ISO: %w", extractErr)
		}
		reg.KernelPath = kernel
		reg.InitrdPath = initrd
		slog.Info("extracted kernel/initrd from ISO", "kernel", kernel, "initrd", initrd)
	}

	// Validate kernel/initrd for kernel type or extracted from ISO.
	if reg.KernelPath != "" || reg.InitrdPath != "" {
		if reg.KernelPath == "" || reg.InitrdPath == "" {
			return db.SandgorgonImage{}, fmt.Errorf("both kernel_path and initrd_path are required (or omit both for ISO auto-extract)")
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

// extractKernelFromISO extracts vmlinuz + initrd from an ISO using bsdtar.
// No mount or privileges required. Files are placed alongside the ISO.
func extractKernelFromISO(isoPath, imageName string) (kernel, initrd string, err error) {
	dir := filepath.Dir(isoPath)

	// Search common kernel/initrd locations inside the ISO.
	kernelCandidates := []string{
		"casper/vmlinuz",          // Ubuntu
		"casper/hwe-vmlinuz",      // Ubuntu HWE
		"live/vmlinuz",            // Debian live
		"images/pxeboot/vmlinuz",  // RHEL/Rocky
		"isolinux/vmlinuz",        // older distros
		"boot/vmlinuz",            // generic
	}
	initrdCandidates := []string{
		"casper/initrd",           // Ubuntu 24.04+
		"casper/initrd.lz",        // Ubuntu older
		"casper/initrd.gz",        // Ubuntu older
		"casper/hwe-initrd",       // Ubuntu HWE
		"live/initrd.img",         // Debian live
		"images/pxeboot/initrd.img", // RHEL/Rocky
		"isolinux/initrd.img",     // older distros
		"boot/initrd.img",         // generic
	}

	// List ISO contents to find which candidates exist.
	listOut, err := exec.Command("bsdtar", "-tf", isoPath).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("listing ISO contents: %s: %w", strings.TrimSpace(string(listOut)), err)
	}
	contents := string(listOut)

	var foundKernel, foundInitrd string
	for _, c := range kernelCandidates {
		if strings.Contains(contents, c) {
			foundKernel = c
			break
		}
	}
	for _, c := range initrdCandidates {
		if strings.Contains(contents, c) {
			foundInitrd = c
			break
		}
	}

	if foundKernel == "" {
		return "", "", fmt.Errorf("no kernel found in ISO (searched: %s)", strings.Join(kernelCandidates, ", "))
	}
	if foundInitrd == "" {
		return "", "", fmt.Errorf("no initrd found in ISO (searched: %s)", strings.Join(initrdCandidates, ", "))
	}

	// Extract to a temp directory, then move to final location.
	tmpDir, err := os.MkdirTemp("", "sandgorgon-iso-*")
	if err != nil {
		return "", "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	extractOut, err := exec.Command("bsdtar", "-xf", isoPath, "-C", tmpDir, foundKernel, foundInitrd).CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("extracting from ISO: %s: %w", strings.TrimSpace(string(extractOut)), err)
	}

	safe := strings.ReplaceAll(imageName, "/", "-")
	dstKernel := filepath.Join(dir, safe+"-vmlinuz")
	dstInitrd := filepath.Join(dir, safe+"-initrd")

	if err := copyFile(filepath.Join(tmpDir, foundKernel), dstKernel); err != nil {
		return "", "", fmt.Errorf("copying kernel: %w", err)
	}
	if err := copyFile(filepath.Join(tmpDir, foundInitrd), dstInitrd); err != nil {
		os.Remove(dstKernel)
		return "", "", fmt.Errorf("copying initrd: %w", err)
	}

	return dstKernel, dstInitrd, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
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
