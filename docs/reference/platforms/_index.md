
# Supported Platforms

Vigo manages servers, workstations, and network devices across 8 platforms. Each page covers bootstrap, package management, available executors, trait collectors, and platform-specific notes.

| Platform | Init System | Package Manager | Executors |
|----------|-------------|-----------------|-----------|
| [Linux](linux.md) | systemd | apt, dnf, yum, apk, pacman, zypper | 43 cross-platform + systemd_dropin, SELinux, logrotate |
| [macOS](macos.md) | launchd | Homebrew | 43 cross-platform + 5 macOS + 6 shared BSD |
| [FreeBSD](freebsd.md) | rc.d | pkg | 43 cross-platform + 4 FreeBSD + 6 shared BSD |
| [OpenBSD](openbsd.md) | rcctl | pkg_add | 43 cross-platform + 1 OpenBSD + 6 shared BSD |
| [NetBSD](netbsd.md) | rc.d | pkgin | 43 cross-platform + 6 shared BSD |
| [illumos](illumos.md) | SMF | IPS | 43 cross-platform + 2 illumos |
| [Windows](windows.md) | Windows Service | Chocolatey, winget, Scoop | 43 cross-platform + 22 Windows |
| [Network Devices](network-devices.md) | N/A (SSH proxy) | N/A | 16 network executors across 5 vendor types |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
