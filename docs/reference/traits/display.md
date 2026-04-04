
# Display Trait

Detects whether the machine has a graphical display server available.

## Fields

| Field | Type | Example | Description |
|-------|------|---------|-------------|
| `display.server` | string | `x11` | Display server type: `x11`, `wayland`, `both`, `none`, `quartz` (macOS), `windows` |

## Values

| Value | Meaning |
|-------|---------|
| `x11` | X11 display server available |
| `wayland` | Wayland compositor available |
| `both` | Both X11 and Wayland available |
| `none` | No display server (headless) |
| `quartz` | macOS (always has Quartz) |
| `windows` | Windows (always has a display server) |

## Detection Method

- **Linux/BSD:** Checks for Xorg/X binaries, Wayland session files, and Xwayland binary. Zero-overhead — filesystem path checks only, no subprocesses.
- **macOS:** Always returns `quartz`.
- **Windows:** Always returns `windows`.

### Linux Detection Paths

| What | Paths Checked |
|------|---------------|
| X11 | `/usr/bin/Xorg`, `/usr/bin/X`, `/usr/local/bin/Xorg`, `/usr/share/xsessions/` (non-empty) |
| Wayland | `/usr/share/wayland-sessions/` (non-empty), `/usr/bin/Xwayland`, `/usr/local/bin/Xwayland` |

## When Expression

Use the `has_display()` builtin to conditionally apply GUI application modules:

```yaml
when: "has_display() and os_family('debian')"
```

`has_display()` returns true when `display.server` is anything other than `none`. It is evaluated **server-side** — headless envoys never receive GUI modules in their policy bundle.

## Example

```yaml
name: waveterm

resources:
  - name: install-waveterm-deb
    type: source_package
    url: "https://github.com/wavetermdev/waveterm/releases/download/v0.14.4/waveterm-linux-{{ .Traits.os.arch }}-0.14.4.deb"
    target_path: /tmp/waveterm-install
    when: "has_display() and os_family('debian')"
```

## Classification

Stable collector — display server availability rarely changes. Cached across check-in cycles.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
