// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package validate

import "testing"

func TestSelfProtect_FileTargetingAgentBinary(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "overwrite-agent",
			Type: "file",
			Attributes: map[string]any{
				"targetpath": "/usr/local/bin/hobnob",
				"content":    "malicious",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file targeting agent binary")
	}
}

func TestSelfProtect_FileTargetingServerBinary(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "overwrite-server",
			Type: "file",
			Attributes: map[string]any{
				"targetpath": "/usr/local/bin/hobd",
				"content":    "malicious",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file targeting server binary")
	}
}

func TestSelfProtect_FileTargetingServerConfigDir(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "overwrite-config",
			Type: "file",
			Attributes: map[string]any{
				"targetpath": "/etc/hobnob-server/server.yaml",
				"content":    "malicious",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file targeting server config dir")
	}
}

func TestSelfProtect_FileTargetingAgentState(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "overwrite-lmdb",
			Type: "file",
			Attributes: map[string]any{
				"targetpath": "/var/lib/hobnob/state/data.mdb",
				"content":    "garbage",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file targeting LMDB state")
	}
}

func TestSelfProtect_FileSafePathAllowed(t *testing.T) {
	mod := Module{
		Name: "good-mod",
		Resources: []Resource{{
			Name: "nginx-conf",
			Type: "file",
			Attributes: map[string]any{
				"targetpath": "/etc/nginx/nginx.conf",
				"content":    "server {}",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if HasErrors(findings) {
		t.Fatalf("safe file should pass: %s", FormatFindings(findings))
	}
}

func TestSelfProtect_ServiceStopAgent(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "kill-agent",
			Type: "service",
			Attributes: map[string]any{
				"service": "hobnob-thrall",
				"state":   "stopped",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for stopping agent service")
	}
}

func TestSelfProtect_ServiceDisableServer(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "disable-server",
			Type: "service",
			Attributes: map[string]any{
				"service": "hobd",
				"state":   "disabled",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for disabling server service")
	}
}

func TestSelfProtect_ServiceRestartAgentAllowed(t *testing.T) {
	mod := Module{
		Name: "good-mod",
		Resources: []Resource{{
			Name: "restart-agent",
			Type: "service",
			Attributes: map[string]any{
				"service": "hobnob-thrall",
				"state":   "restarted",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if HasErrors(findings) {
		t.Fatalf("restarting agent should be allowed: %s", FormatFindings(findings))
	}
}

func TestSelfProtect_ServiceStopUnrelatedAllowed(t *testing.T) {
	mod := Module{
		Name: "good-mod",
		Resources: []Resource{{
			Name: "stop-nginx",
			Type: "service",
			Attributes: map[string]any{
				"service": "nginx",
				"state":   "stopped",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if HasErrors(findings) {
		t.Fatalf("stopping unrelated service should be allowed: %s", FormatFindings(findings))
	}
}

func TestSelfProtect_PackageRemoveHobnob(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "rm-hobnob",
			Type: "package",
			Attributes: map[string]any{
				"package": "hobnob",
				"state":   "absent",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for removing hobnob package")
	}
}

func TestSelfProtect_PackageInstallAllowed(t *testing.T) {
	mod := Module{
		Name: "good-mod",
		Resources: []Resource{{
			Name: "install-hobnob",
			Type: "package",
			Attributes: map[string]any{
				"package": "hobnob",
				"state":   "present",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if HasErrors(findings) {
		t.Fatalf("installing hobnob should be allowed: %s", FormatFindings(findings))
	}
}

func TestSelfProtect_ExecStopAgent(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "exec-kill",
			Type: "exec",
			Attributes: map[string]any{
				"command": "systemctl stop hobnob-thrall",
				"creates": "/tmp/marker",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for exec stopping agent")
	}
}

func TestSelfProtect_ExecStopServer(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "exec-kill-server",
			Type: "exec",
			Attributes: map[string]any{
				"command": "systemctl stop hobd",
				"creates": "/tmp/marker",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for exec stopping server")
	}
}

func TestSelfProtect_ExecSafeCommandAllowed(t *testing.T) {
	mod := Module{
		Name: "good-mod",
		Resources: []Resource{{
			Name: "restart-nginx",
			Type: "exec",
			Attributes: map[string]any{
				"command": "systemctl restart nginx",
				"creates": "/tmp/marker",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if HasErrors(findings) {
		t.Fatalf("safe exec should pass: %s", FormatFindings(findings))
	}
}

func TestSelfProtect_FileLineTargetingAgentConfig(t *testing.T) {
	mod := Module{
		Name: "bad-mod",
		Resources: []Resource{{
			Name: "edit-agent-config",
			Type: "file_line",
			Attributes: map[string]any{
				"path": "/etc/hobnob-thrall/config.yaml",
				"line": "server: evil.example.com",
			},
		}},
	}
	findings := ValidateSelfProtection(mod)
	if !HasErrors(findings) {
		t.Fatal("expected error for file_line targeting agent config")
	}
}

func TestSelfProtect_PathPrefixNotChild(t *testing.T) {
	// /etc/hobnob-serverXYZ should NOT match /etc/hobnob-server/
	result := matchProtectedPath("/etc/hobnob-serverXYZ/foo")
	if result != "" {
		t.Fatalf("should not match similar prefix, got: %s", result)
	}
}

func TestSelfProtect_PathSimilarBinaryAllowed(t *testing.T) {
	// /usr/local/bin/hobnob-helper should NOT match /usr/local/bin/hobnob
	result := matchProtectedPath("/usr/local/bin/hobnob-helper")
	if result != "" {
		t.Fatalf("should not match similar binary name, got: %s", result)
	}
}

func TestValidateTaskCommand_StopAgent(t *testing.T) {
	err := ValidateTaskCommand("systemctl stop hobnob-thrall && echo done")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateTaskCommand_StopServer(t *testing.T) {
	err := ValidateTaskCommand("systemctl stop hobd")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateTaskCommand_RmServerConfig(t *testing.T) {
	err := ValidateTaskCommand("rm -rf /etc/hobnob-server/db")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateTaskCommand_SafeCommand(t *testing.T) {
	err := ValidateTaskCommand("df -h && free -m")
	if err != nil {
		t.Fatalf("safe command should pass: %v", err)
	}
}
