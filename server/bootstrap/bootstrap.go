// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package bootstrap

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"git.alexander4.io/vigo/server/ca"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
)

// validOS is the set of supported operating systems for agent binaries.
var validOS = map[string]bool{
	"linux":   true,
	"darwin":  true,
	"freebsd": true,
	"openbsd": true,
	"netbsd":  true,
	"illumos": true,
	"windows": true,
}

// validArch is the set of supported architectures for agent binaries.
var validArch = map[string]bool{
	"amd64": true,
	"arm64": true,
}

// Handler serves the bootstrap script, agent latest version info, agent
// binary downloads, and bootstrap registration with CSR signing.
type Handler struct {
	serverAddr string
	caFile     string
	agentDir   string
	version    string // server version (fallback)
	// agentVersions maps "<goos>/<goarch>" → version string for every agent
	// binary present in agentDir. Populated at startup by scanning the dist
	// tree. Each binary's version is read from its sidecar `<binary>.version`
	// file written by the build pipeline; the server's own arch falls back to
	// running the binary directly if no sidecar exists. An entry is only
	// present if the version is known; absent entries disable auto-update for
	// that arch (safer than mis-claiming a version and causing update loops).
	agentVersions   map[string]string
	cache           *checksumCache
	metrics         *metrics.Metrics
	ca              *ca.CA
	registry        *registry.Registry
	trustedPatterns []config.TrustedEnrollment
	certValidity    time.Duration
	integrations    *integrations.Dispatcher
	fleetIndex      *fleet.FleetIndex
	licenseEnforcer *license.Enforcer // for post-enrollment capacity reporting
}

// NewHandler creates a Handler for serving bootstrap and agent distribution endpoints.
func NewHandler(serverAddr, caFile, agentDir, version string, m *metrics.Metrics) *Handler {
	h := &Handler{
		serverAddr:    serverAddr,
		caFile:        caFile,
		agentDir:      agentDir,
		version:       version,
		agentVersions: detectAgentVersions(agentDir),
		cache:         newChecksumCache(),
		metrics:       m,
		certValidity:  365 * 24 * time.Hour, // default 1 year
	}
	return h
}

// normalizeArch maps platform-specific architecture names to Go-style names
// used in the agent binary directory layout.
func normalizeArch(arch string) string {
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}

// GoosForFamily maps an agent-reported os.family trait to the Go GOOS
// string used as the dist-tree key. Linux distros report family as the
// vendor ("debian"/"rhel"/"suse"/…) and all map to "linux"; the BSDs,
// illumos, macOS, and Windows report their platform name directly.
// Returns "" when family is empty — the caller must treat that as "unknown
// platform, no expected-version lookup."
//
// Single source of truth for the family→goos mapping, shared by the agent
// auto-update path (server/grpc) and the agent-version currency surface
// (server/fleet, via the gateway resolver). Keeping it in one place guards
// the cross-arch update-loop class of bug that motivated per-arch
// resolution in the first place.
func GoosForFamily(family string) string {
	switch strings.ToLower(family) {
	case "":
		return ""
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	case "freebsd", "openbsd", "netbsd", "illumos":
		return strings.ToLower(family)
	default:
		// Linux distros report family="debian"/"rhel"/"suse"/etc.
		return "linux"
	}
}

// AgentVersion returns the detected agent binary version for the server's
// own architecture, or empty string if no agent binary was found. Used for
// dashboard display and as a backward-compatible accessor; per-arch lookups
// should use AgentVersionForArch.
func (h *Handler) AgentVersion() string {
	return h.agentVersions[runtime.GOOS+"/"+runtime.GOARCH]
}

// AgentVersionForArch returns the detected version of the agent binary for
// the given OS/arch, or empty string if unknown. Empty string signals "do
// not auto-update for this arch" — callers must respect this to avoid
// cross-arch version mismatch update loops.
func (h *Handler) AgentVersionForArch(goos, goarch string) string {
	return h.agentVersions[goos+"/"+goarch]
}

// AgentVersions returns a snapshot of the detected per-arch versions.
// Used by main.go to seed the gRPC server's version map.
func (h *Handler) AgentVersions() map[string]string {
	out := make(map[string]string, len(h.agentVersions))
	for k, v := range h.agentVersions {
		out[k] = v
	}
	return out
}

// DetectAgentVersions scans the dist tree at agentDir and returns a map of
// "<goos>/<goarch>" → version for every agent binary whose version can be
// determined. See detectAgentVersions for the resolution policy.
func DetectAgentVersions(agentDir string) map[string]string {
	return detectAgentVersions(agentDir)
}

// detectAgentVersions walks the supported OS/arch matrix under agentDir and
// resolves each binary's version. Resolution order, per arch:
//
//  1. Read sidecar `<binary>.version` file written by the build pipeline.
//     Works for any arch — no execution required.
//  2. If no sidecar AND the binary is for the server's own arch, run it
//     with `version` (the legacy probe path).
//  3. Otherwise leave the entry absent. Callers treat absent entries as
//     "version unknown — do not advertise updates for this arch."
//
// Logging the per-arch result at startup makes cross-arch version drift
// (the kind that caused the raspberrypi update loop) obvious in the first
// few lines of the server log instead of from a stuck client's perspective.
func detectAgentVersions(agentDir string) map[string]string {
	versions := make(map[string]string)
	matrix := []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"windows", "amd64"}, {"windows", "arm64"},
		{"freebsd", "amd64"}, {"freebsd", "arm64"},
		{"openbsd", "amd64"}, {"openbsd", "arm64"},
		{"netbsd", "amd64"}, {"netbsd", "arm64"},
		{"illumos", "amd64"},
	}
	for _, m := range matrix {
		binPath := BinaryPath(agentDir, m.goos, m.goarch)
		if _, err := os.Stat(binPath); err != nil {
			continue
		}
		v := readVersionSidecar(binPath)
		if v == "" && m.goos == runtime.GOOS && m.goarch == runtime.GOARCH {
			v = probeBinaryVersion(binPath)
		}
		if v != "" {
			versions[m.goos+"/"+m.goarch] = v
			slog.Info("agent binary version detected",
				"os", m.goos, "arch", m.goarch, "version", v)
		} else {
			slog.Warn("agent binary present but version unknown — auto-update disabled for this arch",
				"os", m.goos, "arch", m.goarch, "binary", binPath,
				"hint", "build pipeline should write '<binary>.version' next to each cross-compiled binary")
		}
	}
	return versions
}

// readVersionSidecar reads `<binPath>.version` and returns its trimmed
// contents. Empty string if the file is missing or unreadable.
func readVersionSidecar(binPath string) string {
	data, err := os.ReadFile(binPath + ".version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// probeBinaryVersion runs `<binPath> version` and returns stdout trimmed.
// Only safe to call for the server's own arch — cross-arch invocation
// requires qemu-user-static which we don't depend on.
func probeBinaryVersion(binPath string) string {
	out, err := exec.Command(binPath, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetCA configures the certificate authority for CSR signing during bootstrap.
func (h *Handler) SetCA(c *ca.CA) { h.ca = c }

// SetRegistry configures the enrollment registry for bootstrap registration.
func (h *Handler) SetRegistry(r *registry.Registry) { h.registry = r }

// SetTrustedPatterns configures trusted enrollment patterns for token-free registration.
func (h *Handler) SetTrustedPatterns(patterns []config.TrustedEnrollment) {
	h.trustedPatterns = patterns
}

// SetCertValidity sets the validity duration for signed client certificates.
func (h *Handler) SetCertValidity(d time.Duration) { h.certValidity = d }

// SetIntegrations configures the integrations dispatcher for enrollment events.
func (h *Handler) SetIntegrations(d *integrations.Dispatcher) { h.integrations = d }

// SetFleetIndex configures the in-memory fleet index for immediate envoy availability after bootstrap.
func (h *Handler) SetFleetIndex(fi *fleet.FleetIndex) { h.fleetIndex = fi }

// SetLicenseEnforcer wires the license enforcer so bootstrap responses can
// report post-enrollment capacity (envoys_active, envoys_remaining, warning
// text) back to the agent. The enforcement decision itself happens inside
// the registry under its mutex — this handle is read-only.
func (h *Handler) SetLicenseEnforcer(e *license.Enforcer) { h.licenseEnforcer = e }

// RegisterRoutes registers the bootstrap and agent distribution routes on the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /bootstrap", h.BootstrapScript)
	mux.HandleFunc("GET /api/v1/agent/latest", h.AgentLatest)
	mux.HandleFunc("GET /api/v1/agent/download/{os}/{arch}", h.AgentDownload)
	mux.HandleFunc("GET /api/v1/agent/signing-key", h.SigningKey)
	mux.HandleFunc("POST /api/v1/bootstrap/register", h.BootstrapRegister)
}

// bootstrapData is the template data for the bootstrap shell script.
type bootstrapData struct {
	ServerAddr string
	Token      string
	CACert     string
}

// bootstrapTmpl is the shell script served at GET /bootstrap.
// Token is optional — if empty, the agent uses trusted enrollment (CSR-only).
// All interpolated values are single-quoted to prevent shell injection.
var bootstrapTmpl = template.Must(template.New("bootstrap").Parse(`#!/bin/sh
set -eu

# Vigo Agent Bootstrap Script
# Generated by the Vigo server.

SERVER_ADDR='{{.ServerAddr}}'
TOKEN='{{.Token}}'

# Detect OS and architecture.
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
# illumos reports as "sunos" — map to "illumos".
if [ "$OS" = "sunos" ]; then
    OS="illumos"
fi
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Detected platform: ${OS}/${ARCH}"
echo "Server: ${SERVER_ADDR}"

# Directories.
INSTALL_DIR="/usr/local/sbin"
DATA_DIR="/etc/vigo-envoy"

# Stop existing service and clean up stale state for a fresh bootstrap.
if command -v systemctl > /dev/null 2>&1 && systemctl is-active --quiet vigo-envoy 2>/dev/null; then
    echo "Stopping existing vigo-envoy service..."
    systemctl stop vigo-envoy
elif command -v launchctl > /dev/null 2>&1 && launchctl list com.vigo.envoy > /dev/null 2>&1; then
    echo "Stopping existing vigo-envoy service..."
    launchctl unload /Library/LaunchDaemons/com.vigo.envoy.plist 2>/dev/null || true
elif command -v rcctl > /dev/null 2>&1 && rcctl get vigo_envoy status > /dev/null 2>&1; then
    echo "Stopping existing vigo-envoy service..."
    rcctl stop vigo_envoy 2>/dev/null || true
elif command -v svcadm > /dev/null 2>&1 && svcs -H vigo-envoy > /dev/null 2>&1; then
    echo "Stopping existing vigo-envoy service..."
    svcadm disable -s svc:/system/vigo-envoy:default 2>/dev/null || true
elif [ -x /usr/local/etc/rc.d/vigo_envoy ]; then
    echo "Stopping existing vigo-envoy service..."
    /usr/local/etc/rc.d/vigo_envoy stop 2>/dev/null || true
fi

# Remove old data directory for a clean enrollment.
if [ -d "$DATA_DIR" ]; then
    echo "Removing previous enrollment data..."
    rm -rf "$DATA_DIR"
fi
mkdir -p "$DATA_DIR"

# Remove stale binary at old install path (moved from /usr/local/bin to /usr/local/sbin).
rm -f /usr/local/bin/vigo
# Clear shell command cache so the new path is found immediately.
hash -r 2>/dev/null || true

# Download agent binary (always replaces existing).
echo "Downloading agent binary..."
curl -sSfk "${SERVER_ADDR}/api/v1/agent/download/${OS}/${ARCH}" -o "${INSTALL_DIR}/vigo"
chmod +x "${INSTALL_DIR}/vigo"

# Verify binary integrity via SHA256 checksum.
echo "Verifying binary checksum..."
EXPECTED=$(curl -sSfk "${SERVER_ADDR}/api/v1/agent/latest?os=${OS}&arch=${ARCH}" | \
    sed -n 's/.*"sha256" *: *"\([a-f0-9]*\)".*/\1/p')
if [ -n "$EXPECTED" ]; then
    if command -v sha256sum > /dev/null 2>&1; then
        ACTUAL=$(sha256sum "${INSTALL_DIR}/vigo" | cut -d' ' -f1)
    elif command -v shasum > /dev/null 2>&1; then
        ACTUAL=$(shasum -a 256 "${INSTALL_DIR}/vigo" | cut -d' ' -f1)
    elif command -v digest > /dev/null 2>&1; then
        ACTUAL=$(digest -a sha256 "${INSTALL_DIR}/vigo")
    else
        echo "WARNING: no SHA256 tool found, skipping checksum verification"
        ACTUAL="$EXPECTED"
    fi
    if [ "$ACTUAL" != "$EXPECTED" ]; then
        echo "FATAL: checksum mismatch — binary may be corrupted or tampered with"
        echo "  expected: ${EXPECTED}"
        echo "  actual:   ${ACTUAL}"
        rm -f "${INSTALL_DIR}/vigo"
        exit 1
    fi
    echo "Checksum verified."
else
    echo "WARNING: server did not return a checksum, skipping verification"
fi

# Download license and commercial terms into the agent data directory.
echo "Downloading license terms..."
curl -sSfk "${SERVER_ADDR}/license/LICENSE" -o "${DATA_DIR}/LICENSE" 2>/dev/null || true
curl -sSfk "${SERVER_ADDR}/license/COMMERCIAL-TERMS.md" -o "${DATA_DIR}/COMMERCIAL-TERMS.md" 2>/dev/null || true

# Bootstrap the agent (generates keys, CSR, registers with server, receives certs).
# Always pass --force: this script cleans local state above, so re-enrollment is expected.
echo "Bootstrapping agent..."
BOOTSTRAP_ARGS="--server ${SERVER_ADDR} --force"
if [ -n "$TOKEN" ]; then
    BOOTSTRAP_ARGS="${BOOTSTRAP_ARGS} --token ${TOKEN}"
fi
"${INSTALL_DIR}/vigo" bootstrap ${BOOTSTRAP_ARGS}

# Extract gRPC server address (strip protocol and port, add :1530).
# Install service based on detected init system.
# The agent reads server address from ${DATA_DIR}/config written during bootstrap.
# No --server flag needed — the config file is the single source of truth.
if command -v systemctl > /dev/null 2>&1; then
    # Linux (systemd)
    cat > /etc/systemd/system/vigo-envoy.service << SYSTEMD
[Unit]
Description=Vigo State Enforcement Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/sbin/vigo
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
SYSTEMD
    systemctl daemon-reload
    systemctl enable vigo-envoy
    systemctl start vigo-envoy
    echo "Vigo agent service installed and started (systemd)."
elif command -v launchctl > /dev/null 2>&1; then
    # macOS (launchd) — uses /usr/local/etc/vigo-envoy as data dir.
    DATA_DIR="/usr/local/etc/vigo-envoy"
    mkdir -p "$DATA_DIR"
    cat > /Library/LaunchDaemons/com.vigo.envoy.plist << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.vigo.envoy</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/sbin/vigo</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
</dict>
</plist>
PLIST
    launchctl load -w /Library/LaunchDaemons/com.vigo.envoy.plist
    echo "Vigo agent service installed and started (launchd)."
elif command -v rcctl > /dev/null 2>&1; then
    # OpenBSD (rcctl)
    cat > /etc/rc.d/vigo_envoy << 'RCSCRIPT'
#!/bin/ksh
daemon="/usr/local/sbin/vigo"
daemon_flags=""
. /etc/rc.d/rc.subr
rc_cmd $1
RCSCRIPT
    chmod 755 /etc/rc.d/vigo_envoy
    rcctl enable vigo_envoy
    rcctl start vigo_envoy
    echo "Vigo agent service installed and started (rcctl)."
elif command -v svccfg > /dev/null 2>&1 && command -v svcadm > /dev/null 2>&1; then
    # illumos (SMF)
    cat > /var/svc/manifest/system/vigo-envoy.xml << SMFMANIFEST
<?xml version="1.0"?>
<!DOCTYPE service_bundle SYSTEM "/usr/share/lib/xml/dtd/service_bundle.dtd.1">
<service_bundle type="manifest" name="vigo-envoy">
  <service name="system/vigo-envoy" type="service" version="1">
    <create_default_instance enabled="false"/>
    <single_instance/>
    <dependency name="network" grouping="require_all" restart_on="error" type="service">
      <service_fmri value="svc:/milestone/network:default"/>
    </dependency>
    <dependency name="filesystem" grouping="require_all" restart_on="error" type="service">
      <service_fmri value="svc:/system/filesystem/local"/>
    </dependency>
    <exec_method type="method" name="start"
      exec="/usr/local/sbin/vigo &amp;"
      timeout_seconds="60">
      <method_context><method_credential user="root" group="root"/></method_context>
    </exec_method>
    <exec_method type="method" name="stop" exec=":kill" timeout_seconds="60"/>
    <template>
      <common_name><loctext xml:lang="C">Vigo State Enforcement Agent</loctext></common_name>
    </template>
  </service>
</service_bundle>
SMFMANIFEST
    svccfg import /var/svc/manifest/system/vigo-envoy.xml
    svcadm enable svc:/system/vigo-envoy:default
    echo "Vigo agent service installed and started (SMF)."
elif [ -d /etc/rc.d ] && [ -f /etc/rc.subr ]; then
    # FreeBSD / NetBSD (rc.d)
    RCDIR="/usr/local/etc/rc.d"
    if [ "$(uname -s)" = "NetBSD" ]; then
        RCDIR="/etc/rc.d"
    fi
    mkdir -p "$RCDIR"
    cat > "${RCDIR}/vigo_envoy" << RCSCRIPT
#!/bin/sh
# PROVIDE: vigo_envoy
# REQUIRE: NETWORKING
# KEYWORD: shutdown
. /etc/rc.subr
name="vigo_envoy"
rcvar="\${name}_enable"
command="/usr/local/sbin/vigo"
command_args="&"
pidfile="/var/run/\${name}.pid"
load_rc_config \$name
: \${vigo_envoy_enable:=NO}
run_rc_command "\$1"
RCSCRIPT
    chmod 755 "${RCDIR}/vigo_envoy"
    if command -v sysrc > /dev/null 2>&1; then
        sysrc vigo_envoy_enable=YES
    else
        grep -q 'vigo_envoy=YES' /etc/rc.conf 2>/dev/null || echo 'vigo_envoy=YES' >> /etc/rc.conf
    fi
    service vigo_envoy start 2>/dev/null || "${RCDIR}/vigo_envoy" start
    echo "Vigo agent service installed and started (rc.d)."
else
    echo "No supported init system found. Start the agent manually:"
    echo "  vigo"
fi

echo "Bootstrap complete."
`))

// windowsBootstrapTmpl is the PowerShell script served at GET /bootstrap?os=windows.
// Uses double-quoted Go string to avoid backtick conflicts with PowerShell syntax.
var windowsBootstrapTmpl = template.Must(template.New("bootstrap-windows").Parse("# Vigo Agent Bootstrap Script (Windows)\n# Generated by the Vigo server.\n# Usage (PowerShell 7+): irm {{.ServerAddr}}/bootstrap?os=windows -SkipCertificateCheck | iex\n# Usage (PowerShell 5): [Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}; irm {{.ServerAddr}}/bootstrap?os=windows | iex\n\n$ErrorActionPreference = 'Stop'\n\n# Trust the Vigo server's certificate for this session (equivalent to curl -k).\n# Bootstrap runs before the agent has TLS certs, so we must skip verification\n# just like the Linux script does with curl -sSfk.\n$TlsSkip = @{}\nif ($PSVersionTable.PSVersion.Major -ge 6) {\n    $TlsSkip = @{ SkipCertificateCheck = $true }\n} else {\n    Add-Type @'\n    using System.Net;\n    using System.Net.Security;\n    using System.Security.Cryptography.X509Certificates;\n    public class VigoTrustAll {\n        public static void Enable() {\n            ServicePointManager.ServerCertificateValidationCallback =\n                delegate { return true; };\n        }\n    }\n'@\n    [VigoTrustAll]::Enable()\n}\n\n$ServerAddr = '{{.ServerAddr}}'\n$Token = '{{.Token}}'\n\n# Detect architecture.\n$Arch = switch ($env:PROCESSOR_ARCHITECTURE) {\n    'AMD64' { 'amd64' }\n    'ARM64' { 'arm64' }\n    default { Write-Error \"Unsupported architecture: $env:PROCESSOR_ARCHITECTURE\"; exit 1 }\n}\n\nWrite-Host \"Detected platform: windows/$Arch\"\nWrite-Host \"Server: $ServerAddr\"\n\n# Create directories.\n$InstallDir = Join-Path $env:ProgramFiles 'Vigo'\n$DataDir = Join-Path $env:ProgramData 'Vigo'\n\n# Stop existing service and clean up for fresh bootstrap.\n$ServiceName = 'vigo-envoy'\nif (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {\n    Write-Host 'Stopping existing vigo-envoy service...'\n    Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue\n    sc.exe delete $ServiceName | Out-Null\n    Start-Sleep -Seconds 2\n}\nif (Test-Path $DataDir) {\n    Write-Host 'Removing previous enrollment data...'\n    Remove-Item -Recurse -Force $DataDir\n}\nNew-Item -ItemType Directory -Force -Path $InstallDir | Out-Null\nNew-Item -ItemType Directory -Force -Path $DataDir | Out-Null\n\n# Download agent binary (always replaces existing).\nWrite-Host 'Downloading agent binary...'\n$BinPath = Join-Path $InstallDir 'vigo.exe'\nInvoke-WebRequest -Uri \"$ServerAddr/api/v1/agent/download/windows/$Arch\" -OutFile $BinPath @TlsSkip\n\n# Verify binary integrity via SHA256 checksum.\nWrite-Host 'Verifying binary checksum...'\ntry {\n    $Latest = Invoke-RestMethod -Uri \"$ServerAddr/api/v1/agent/latest?os=windows&arch=$Arch\" @TlsSkip\n    $Expected = $Latest.sha256\n    if ($Expected) {\n        $Actual = (Get-FileHash -Path $BinPath -Algorithm SHA256).Hash.ToLower()\n        if ($Actual -ne $Expected) {\n            Write-Error \"FATAL: checksum mismatch -- binary may be corrupted or tampered with`nexpected: $Expected`nactual:   $Actual\"\n            Remove-Item -Force $BinPath\n            exit 1\n        }\n        Write-Host 'Checksum verified.'\n    } else {\n        Write-Host 'WARNING: server did not return a checksum, skipping verification'\n    }\n} catch {\n    Write-Host \"WARNING: could not verify checksum: $_\"\n}\n\n# Download license and commercial terms into the agent data directory.\nWrite-Host 'Downloading license terms...'\ntry { Invoke-WebRequest -Uri \"$ServerAddr/license/LICENSE\" -OutFile (Join-Path $DataDir 'LICENSE') @TlsSkip } catch {}\ntry { Invoke-WebRequest -Uri \"$ServerAddr/license/COMMERCIAL-TERMS.md\" -OutFile (Join-Path $DataDir 'COMMERCIAL-TERMS.md') @TlsSkip } catch {}\n\n# Bootstrap the agent (generates keys, CSR, registers with server, receives certs).\nWrite-Host 'Bootstrapping agent...'\n$BootstrapArgs = @('bootstrap', '--server', $ServerAddr, '--force')\nif ($Token) { $BootstrapArgs += @('--token', $Token) }\n& $BinPath @BootstrapArgs\n\n# Install Windows service.\n# The agent reads server address from the config file written during bootstrap.\nWrite-Host 'Installing Windows service...'\n$ServiceName = 'vigo-envoy'\n$BinArgs = ('\"' + $BinPath + '\"')\n\nsc.exe create $ServiceName binPath= $BinArgs start= auto | Out-Null\nsc.exe description $ServiceName 'Vigo State Enforcement Agent' | Out-Null\nsc.exe failure $ServiceName reset= 86400 actions= restart/60000/restart/60000/restart/60000 | Out-Null\nsc.exe start $ServiceName | Out-Null\n\nWrite-Host 'Vigo agent service installed and started.'\nWrite-Host \"  sc.exe query $ServiceName    # check status\"\nWrite-Host 'Bootstrap complete.'\n"))

// BootstrapScript serves a shell script that installs the agent and enrolls
// the envoy. Token is optional — if omitted, the agent uses trusted enrollment.
// If os=windows is specified, serves a PowerShell script instead.
func (h *Handler) BootstrapScript(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")

	// Read CA certificate content for embedding (if agent needs it for
	// initial HTTPS to the REST API, the bootstrap command fetches it).
	var caCertStr string
	if caCert, err := os.ReadFile(h.caFile); err == nil {
		caCertStr = string(caCert)
	}

	// Derive the server address from the request if the configured address
	// is just a port (e.g. ":8443"). The envoy needs a full URL to curl from.
	serverAddr := h.serverAddr
	if strings.HasPrefix(serverAddr, ":") {
		// Derive from request Host header, but only use the hostname portion
		// to prevent header injection.
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		serverAddr = "https://" + host + h.serverAddr
	}

	data := bootstrapData{
		ServerAddr: serverAddr,
		Token:      token,
		CACert:     caCertStr,
	}

	// Detect Windows: explicit query param or User-Agent heuristic.
	reqOS := r.URL.Query().Get("os")
	isWindows := reqOS == "windows" || strings.Contains(r.UserAgent(), "PowerShell")

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "inline")

	tmpl := bootstrapTmpl
	if isWindows {
		tmpl = windowsBootstrapTmpl
	}
	if err := tmpl.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap template execute: %v\n", err)
	}
}

// bootstrapRegisterRequest is the JSON body for POST /api/v1/bootstrap/register.
type bootstrapRegisterRequest struct {
	Hostname  string `json:"hostname"`
	IPAddress string `json:"ip_address"`
	PubkeyPEM string `json:"pubkey_pem"`
	CSRPEM    string `json:"csr_pem"`
	Token     string `json:"token,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

// bootstrapRegisterResponse is the JSON response for POST /api/v1/bootstrap/register.
// EnvoysRemaining / LicenseMax / LicenseWarning communicate capacity posture
// back to the envoy so the curl one-liner can print an early warning when the
// fleet is approaching its license cap.
type bootstrapRegisterResponse struct {
	EnvoyID         string `json:"envoy_id"`
	SignedCertPEM   string `json:"signed_cert_pem"`
	CACertPEM       string `json:"ca_cert_pem"`
	EnvoysActive    int    `json:"envoys_active,omitempty"`
	EnvoysRemaining int    `json:"envoys_remaining,omitempty"`
	LicenseMax      int    `json:"license_max,omitempty"`
	LicenseWarning  string `json:"license_warning,omitempty"`
}

// BootstrapRegister handles agent enrollment with CSR signing. If a token is
// provided, it uses the standard token+pattern flow. If no token is provided,
// it checks trusted enrollment patterns with CIDR validation.
func (h *Handler) BootstrapRegister(w http.ResponseWriter, r *http.Request) {
	var req bootstrapRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.Hostname == "" || req.PubkeyPEM == "" || req.CSRPEM == "" {
		http.Error(w, "hostname, pubkey_pem, and csr_pem are required", http.StatusBadRequest)
		return
	}

	if h.ca == nil || h.registry == nil {
		http.Error(w, "bootstrap registration not configured (CA key or registry not loaded)", http.StatusServiceUnavailable)
		return
	}

	// Extract peer IP for CIDR validation.
	peerIP := extractIP(r.RemoteAddr)

	slog.Info("bootstrap register request",
		"hostname", req.Hostname,
		"ip", req.IPAddress,
		"peer_ip", peerIP,
		"has_token", req.Token != "",
	)

	var envoyID string
	var err error

	if req.Token != "" {
		// Token-based enrollment (existing flow).
		envoyID, err = h.registry.Register(r.Context(), req.Hostname, req.IPAddress, req.PubkeyPEM, req.Token, req.Force)
	} else {
		// Trusted enrollment (no token — validate hostname+CIDR).
		if len(h.trustedPatterns) == 0 {
			http.Error(w, "no token provided and no trusted enrollment patterns configured", http.StatusForbidden)
			return
		}
		envoyID, err = h.registry.TrustedRegister(r.Context(), req.Hostname, req.IPAddress, req.PubkeyPEM, h.trustedPatterns, peerIP, req.Force)
	}

	if err != nil {
		slog.Warn("bootstrap registration failed",
			"hostname", req.Hostname,
			"error", err,
		)
		h.metrics.EnrollmentsTotal.WithLabelValues("denied").Inc()
		// Surface license-gate rejections with 402 Payment Required so the
		// curl one-liner can distinguish "licence full" from generic denial
		// and print an actionable message. Other failures stay at 403.
		code := http.StatusForbidden
		switch {
		case errors.Is(err, license.ErrNodeLimitReached):
			code = http.StatusPaymentRequired
		case errors.Is(err, license.ErrEnrollmentBlocked),
			errors.Is(err, license.ErrServiceStopping),
			errors.Is(err, license.ErrCountUnavailable):
			code = http.StatusPaymentRequired
		}
		http.Error(w, err.Error(), code)
		return
	}

	// Sign the CSR.
	signedCert, err := h.ca.SignCSR([]byte(req.CSRPEM), req.Hostname, h.certValidity)
	if err != nil {
		slog.Error("CSR signing failed", "hostname", req.Hostname, "error", err)
		h.metrics.EnrollmentsTotal.WithLabelValues("error").Inc()
		http.Error(w, "registration failed", http.StatusInternalServerError)
		return
	}

	// Pin the signed cert's SPKI so the check-in verify path can reject a
	// TLS-terminating proxy/MITM presenting a different cert (cert-pinning).
	// Best-effort: a failure here leaves the envoy grandfathered (verify path
	// skips enforcement), not unenrolled — enrollment already succeeded.
	if spki, perr := certPinFromPEM(signedCert); perr != nil {
		slog.Warn("bootstrap: could not derive cert pin from signed cert; envoy grandfathered",
			"hostname", req.Hostname, "envoy_id", envoyID, "error", perr)
	} else if perr := h.registry.SetEnvoyCertPin(r.Context(), envoyID, spki); perr != nil {
		slog.Warn("bootstrap: could not store cert pin; envoy grandfathered",
			"hostname", req.Hostname, "envoy_id", envoyID, "error", perr)
	}

	h.metrics.EnrollmentsTotal.WithLabelValues("success").Inc()
	slog.Info("envoy bootstrapped",
		"hostname", req.Hostname,
		"envoy_id", envoyID,
	)

	// FleetIndex insertion is now handled inside registry.Register /
	// registry.TrustedRegister under the enrollment mutex, so this handler
	// no longer calls AddEnvoy directly — doing so here would double-count.

	// Dispatch enrollment event to integrations.
	if h.integrations != nil {
		h.integrations.Dispatch(integrations.Event{
			Name:      "envoy.enrolled",
			Timestamp: time.Now(),
			Hostname:  req.Hostname,
			EnvoyID:   envoyID,
			Summary:   fmt.Sprintf("%s enrolled via bootstrap as %s", req.Hostname, envoyID),
			Details:   map[string]any{"ip": req.IPAddress},
		})
	}

	resp := bootstrapRegisterResponse{
		EnvoyID:       envoyID,
		SignedCertPEM: string(signedCert),
		CACertPEM:     string(h.ca.CACertPEM()),
	}

	// Populate post-enrollment capacity information so the agent-side
	// bootstrap script can print a heads-up as the fleet approaches the
	// license cap. Pure read — no enforcement action here, the gate already
	// ran inside the registry.
	if h.licenseEnforcer != nil {
		if st := h.licenseEnforcer.GetStatus(); st.MaxEnvoys > 0 {
			// Mode-aware count (local FleetIndex locally, spanner-wide in
			// aggregate mode) — the licensed-count SoT, same value every
			// other license surface reports.
			active := st.ActiveEnvoys
			remaining := st.MaxEnvoys - active
			if remaining < 0 {
				remaining = 0
			}
			resp.EnvoysActive = active
			resp.LicenseMax = st.MaxEnvoys
			resp.EnvoysRemaining = remaining
			if remaining == 0 {
				resp.LicenseWarning = fmt.Sprintf("license at capacity: %d/%d — next enrollment will be rejected", active, st.MaxEnvoys)
			} else if remaining*10 <= st.MaxEnvoys { // within 10% of cap
				resp.LicenseWarning = fmt.Sprintf("license nearing capacity: %d/%d — %d slot(s) remaining", active, st.MaxEnvoys, remaining)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// latestResponse is the JSON response for GET /api/v1/agent/latest.
type latestResponse struct {
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Signature   string `json:"signature,omitempty"`
	DownloadURL string `json:"download_url"`
}

// AgentLatest returns JSON with the current agent version, SHA256 checksum,
// ED25519 signature (if available), and download URL for the specified OS and architecture.
//
// The advertised Version MUST match the compiled-in version of the binary
// served at the matching DownloadURL — otherwise an agent that downloads the
// binary, execs into it, and reports its compiled-in version on the next
// check-in will look "out of date" again, triggering an infinite update loop.
// We resolve Version per-arch from the agentVersions map populated at startup.
// If the version is unknown for this arch, we return Version="" which the
// client treats as "no update available" — safer than guessing.
func (h *Handler) AgentLatest(w http.ResponseWriter, r *http.Request) {
	goos := r.URL.Query().Get("os")
	goarch := normalizeArch(r.URL.Query().Get("arch"))
	if goos == "" {
		goos = "linux"
	}
	if goarch == "" {
		goarch = "amd64"
	}

	resp := latestResponse{
		DownloadURL: fmt.Sprintf("/api/v1/agent/download/%s/%s", goos, goarch),
	}

	// Try to compute checksum for the requested binary.
	binPath := BinaryPath(h.agentDir, goos, goarch)
	sum, err := h.cache.Get(binPath)
	if err != nil {
		// Binary not available — Version stays empty, signaling no update.
		slog.Info("agent_latest: binary not available",
			"os", goos, "arch", goarch, "binary", binPath, "error", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp.SHA256 = sum

	// Resolve Version PER-ARCH so we never advertise a version that doesn't
	// match the binary we're about to serve.
	resp.Version = h.agentVersions[goos+"/"+goarch]
	if resp.Version == "" {
		// Binary present but version unknown. Don't claim a version we
		// can't verify — the client will see Version="" and skip the update.
		slog.Warn("agent_latest: binary available but version unknown — skipping update advertisement",
			"os", goos, "arch", goarch, "binary", binPath, "sha256", sum,
			"hint", "build pipeline should write '<binary>.version' next to each cross-compiled binary")
	}

	// Read ED25519 signature from adjacent .sig file. Missing or
	// unreadable .sig means we serve no signature, agents at ADR-031
	// fail-closed on empty signature, and auto-update silently breaks.
	// Surface the omission once per request so the diagnostic gap that
	// turned a missing-.sig deployment into a 2-hour investigation
	// can't recur.
	if sig, _, err := ReadSigFile(binPath + ".sig"); err == nil {
		resp.Signature = sig
	} else {
		slog.Warn("agent_latest: serving without signature — ADR-031 agents will refuse auto-update",
			"os", goos, "arch", goarch, "binary", binPath, "error", err)
	}

	slog.Info("agent_latest: served",
		"os", goos, "arch", goarch, "version", resp.Version, "sha256", sum)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// SigningKey returns the hex-encoded ED25519 public key from the first available
// agent .sig file. Served for bootstrap scripts and operator debugging. The
// agent self-update path no longer consumes this endpoint — per ADR-031 the
// agent verifies updates against a pubkey baked in at build time
// (`VIGO_AGENT_SIGNING_PUBKEY`) rather than trusting whatever pubkey the
// server returns.
func (h *Handler) SigningKey(w http.ResponseWriter, r *http.Request) {
	// Walk known platforms to find any .sig file.
	for _, goos := range []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "illumos", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			sigPath := BinaryPath(h.agentDir, goos, goarch) + ".sig"
			if _, pubKey, err := ReadSigFile(sigPath); err == nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"public_key": pubKey})
				return
			}
		}
	}
	http.Error(w, "no signing key available", http.StatusNotFound)
}

// AgentDownload serves the pre-built agent binary for the specified OS and
// architecture from the agent distribution directory.
func (h *Handler) AgentDownload(w http.ResponseWriter, r *http.Request) {
	goos := r.PathValue("os")
	goarch := r.PathValue("arch")

	// Validate OS and arch.
	if !validOS[goos] || !validArch[goarch] {
		http.Error(w, fmt.Sprintf("unsupported platform: %s/%s (supported: linux|darwin|freebsd|openbsd|netbsd|illumos|windows / amd64|arm64)", goos, goarch), http.StatusBadRequest)
		return
	}

	binPath := filepath.Clean(BinaryPath(h.agentDir, goos, goarch))

	// Check existence before serving.
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		http.Error(w, "agent binary not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=vigo-%s-%s", goos, goarch))
	http.ServeFile(w, r, binPath)
}

// extractIP extracts the IP address from a host:port string.
func extractIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// certPinFromPEM returns the hex sha256 of a PEM-encoded certificate's
// SubjectPublicKeyInfo — the cert-pin value stored at enrollment and compared
// against the live TLS peer cert on every check-in.
func certPinFromPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("no PEM block in signed certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing signed certificate: %w", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:]), nil
}
