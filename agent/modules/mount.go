package modules

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// MountExecutor manages filesystem mounts and /etc/fstab entries.
// Params:
//   - path (or name): mount point
//   - device: block device or remote path (required for mounted/present)
//   - fstype: filesystem type (required for mounted/present)
//   - options: mount options (default "defaults")
//   - dump: fstab dump field (default "0")
//   - pass: fstab pass field (default "0")
//   - state: mounted (ensure fstab + mount), unmounted (ensure fstab, unmount),
//            present (fstab only), absent (remove fstab + unmount)
type MountExecutor struct {
	FstabFile string // override for testing (default /etc/fstab)
	Runner    CommandRunner
}

func (e *MountExecutor) fstabFile() string {
	if e.FstabFile != "" {
		return e.FstabFile
	}
	return "/etc/fstab"
}

func (e *MountExecutor) runner() CommandRunner {
	if e.Runner != nil {
		return e.Runner
	}
	return &DefaultRunner{}
}

func (e *MountExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	mountPath := params["path"]
	if mountPath == "" {
		mountPath = params["name"]
	}
	device := params["device"]
	fstype := params["fstype"]
	options := params["options"]
	if options == "" {
		options = "defaults"
	}
	dump := params["dump"]
	if dump == "" {
		dump = "0"
	}
	pass := params["pass"]
	if pass == "" {
		pass = "0"
	}
	state := params["state"]
	if state == "" {
		state = "mounted"
	}

	result := ResourceResult{
		ResourceType: "mount",
		ResourceName: mountPath,
	}
	defer func() { result.DurationMs = int32(time.Since(start).Milliseconds()) }()

	if mountPath == "" {
		result.Error = fmt.Errorf("mount executor: path or name param is required")
		return result
	}

	switch state {
	case "mounted", "present":
		if device == "" || fstype == "" {
			result.Error = fmt.Errorf("mount executor: device and fstype params are required for state=%s", state)
			return result
		}
		return e.ensureMounted(ctx, mountPath, device, fstype, options, dump, pass, state == "mounted", dryRun, result)
	case "unmounted":
		return e.ensureUnmounted(ctx, mountPath, dryRun, result)
	case "absent":
		return e.ensureAbsent(ctx, mountPath, dryRun, result)
	default:
		result.Error = fmt.Errorf("mount executor: unknown state %q", state)
		return result
	}
}

func (e *MountExecutor) ensureMounted(ctx context.Context, path, device, fstype, options, dump, pass string, doMount, dryRun bool, result ResourceResult) ResourceResult {
	desiredEntry := fstabEntry{device, path, fstype, options, dump, pass}
	desiredLine := desiredEntry.String()

	// Check fstab.
	entries, rawLines, err := parseFstab(e.fstabFile())
	if err != nil && !os.IsNotExist(err) {
		result.Error = fmt.Errorf("reading fstab: %w", err)
		return result
	}

	fstabCorrect := false
	for _, entry := range entries {
		if entry.mountpoint == path && entry.String() == desiredLine {
			fstabCorrect = true
			break
		}
	}

	// Check if currently mounted.
	mounted := e.isMounted(ctx, path)

	if fstabCorrect && (!doMount || mounted) {
		result.Action = ActionNone
		return result
	}

	result.Changed = true

	// Determine action.
	existsInFstab := false
	for _, entry := range entries {
		if entry.mountpoint == path {
			existsInFstab = true
			break
		}
	}
	if existsInFstab {
		result.Action = ActionUpdated
	} else {
		result.Action = ActionCreated
	}

	if dryRun {
		return result
	}

	// Update fstab if needed.
	if !fstabCorrect {
		var newLines []string
		replaced := false
		for _, line := range rawLines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				fields := strings.Fields(trimmed)
				if len(fields) >= 2 && fields[1] == path {
					newLines = append(newLines, desiredLine)
					replaced = true
					continue
				}
			}
			newLines = append(newLines, line)
		}
		if !replaced {
			newLines = append(newLines, desiredLine)
		}
		if err := writeLines(e.fstabFile(), newLines); err != nil {
			result.Error = fmt.Errorf("writing fstab: %w", err)
			return result
		}
	}

	// Mount if requested and not already mounted.
	if doMount && !mounted {
		if err := os.MkdirAll(path, 0755); err != nil {
			result.Error = fmt.Errorf("creating mount point %s: %w", path, err)
			return result
		}
		if _, err := e.runner().Run(ctx, "mount", path); err != nil {
			result.Error = fmt.Errorf("mount %s: %w", path, err)
		}
	}

	return result
}

func (e *MountExecutor) ensureUnmounted(ctx context.Context, path string, dryRun bool, result ResourceResult) ResourceResult {
	if !e.isMounted(ctx, path) {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionUpdated
	result.Changed = true
	if !dryRun {
		if _, err := e.runner().Run(ctx, "umount", path); err != nil {
			result.Error = fmt.Errorf("umount %s: %w", path, err)
		}
	}
	return result
}

func (e *MountExecutor) ensureAbsent(ctx context.Context, path string, dryRun bool, result ResourceResult) ResourceResult {
	// Remove from fstab.
	entries, rawLines, err := parseFstab(e.fstabFile())
	if err != nil && !os.IsNotExist(err) {
		result.Error = fmt.Errorf("reading fstab: %w", err)
		return result
	}

	inFstab := false
	for _, entry := range entries {
		if entry.mountpoint == path {
			inFstab = true
			break
		}
	}

	mounted := e.isMounted(ctx, path)

	if !inFstab && !mounted {
		result.Action = ActionNone
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	// Unmount first.
	if mounted {
		if _, err := e.runner().Run(ctx, "umount", path); err != nil {
			result.Error = fmt.Errorf("umount %s: %w", path, err)
			return result
		}
	}

	// Remove from fstab.
	if inFstab {
		var newLines []string
		for _, line := range rawLines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				fields := strings.Fields(trimmed)
				if len(fields) >= 2 && fields[1] == path {
					continue
				}
			}
			newLines = append(newLines, line)
		}
		if err := writeLines(e.fstabFile(), newLines); err != nil {
			result.Error = fmt.Errorf("writing fstab: %w", err)
		}
	}

	return result
}

func (e *MountExecutor) isMounted(ctx context.Context, path string) bool {
	out, err := e.runner().Run(ctx, "findmnt", "-n", "-o", "TARGET", path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == path
}

type fstabEntry struct {
	device     string
	mountpoint string
	fstype     string
	options    string
	dump       string
	pass       string
}

func (f fstabEntry) String() string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", f.device, f.mountpoint, f.fstype, f.options, f.dump, f.pass)
}

func parseFstab(path string) ([]fstabEntry, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var entries []fstabEntry
	var rawLines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		rawLines = append(rawLines, line)

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		entry := fstabEntry{
			device:     fields[0],
			mountpoint: fields[1],
			fstype:     fields[2],
		}
		if len(fields) >= 4 {
			entry.options = fields[3]
		}
		if len(fields) >= 5 {
			entry.dump = fields[4]
		}
		if len(fields) >= 6 {
			entry.pass = fields[5]
		}
		entries = append(entries, entry)
	}
	return entries, rawLines, scanner.Err()
}
