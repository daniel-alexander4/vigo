package nails

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
)

// PackageCollector gathers information about installed packages.
type PackageCollector struct{}

// Name returns "packages".
func (c *PackageCollector) Name() string { return "packages" }

// Collect returns installed packages as {"installed": {"name": "version", ...}}.
// Returns an empty map on unsupported platforms or if the query fails.
// Package collection is informational and non-fatal.
func (c *PackageCollector) Collect(_ context.Context) (map[string]any, error) {
	installed := make(map[string]string)

	switch runtime.GOOS {
	case "linux":
		installed = c.collectLinux()
	case "darwin":
		installed = c.collectDarwin()
	}

	return map[string]any{
		"installed": installed,
	}, nil
}

// collectLinux tries dpkg-query first (Debian/Ubuntu), then rpm (RHEL/Fedora).
func (c *PackageCollector) collectLinux() map[string]string {
	// Try dpkg-query (Debian/Ubuntu family).
	if pkgs := c.runDpkg(); pkgs != nil {
		return pkgs
	}
	// Try rpm (RHEL/Fedora family).
	if pkgs := c.runRpm(); pkgs != nil {
		return pkgs
	}
	return make(map[string]string)
}

// collectDarwin tries brew list.
func (c *PackageCollector) collectDarwin() map[string]string {
	if pkgs := c.runBrew(); pkgs != nil {
		return pkgs
	}
	return make(map[string]string)
}

// runDpkg queries installed packages via dpkg-query.
func (c *PackageCollector) runDpkg() map[string]string {
	cmd := exec.Command("dpkg-query", "-W", "-f", "${Package}\t${Version}\n")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return c.parseTSV(out)
}

// runRpm queries installed packages via rpm.
func (c *PackageCollector) runRpm() map[string]string {
	cmd := exec.Command("rpm", "-qa", "--queryformat", "%{NAME}\t%{VERSION}-%{RELEASE}\n")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return c.parseTSV(out)
}

// runBrew queries installed packages via brew.
func (c *PackageCollector) runBrew() map[string]string {
	cmd := exec.Command("brew", "list", "--versions")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	// brew outputs "name version1 version2..." — take the first version.
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			result[fields[0]] = fields[1]
		}
	}
	return result
}

// parseTSV parses tab-separated "name\tversion" lines.
func (c *PackageCollector) parseTSV(data []byte) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}
