// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package sandgorgon

import (
	"fmt"
	"strings"
	"text/template"

	"git.alexander4.io/vigo/server/db"
)

// iPXE boot script templates per preseed type.
// These generate chainloadable iPXE scripts that boot from HTTP-served images.

// GenerateIPXEScript creates an iPXE boot script for the given image and server address.
func GenerateIPXEScript(image db.SandgorgonImage, serverAddr, hostname, enrollToken string) (string, error) {
	var sb strings.Builder

	sb.WriteString("#!ipxe\n\n")
	sb.WriteString(fmt.Sprintf("# Sandgorgon provisioning: %s\n", image.Name))
	sb.WriteString(fmt.Sprintf("# Target: %s\n\n", hostname))

	baseURL := fmt.Sprintf("%s/sandgorgon/images/%s", serverAddr, image.ID)

	switch image.ImageType {
	case "kernel":
		// Direct kernel boot.
		sb.WriteString(fmt.Sprintf("kernel %s/kernel", baseURL))
		if image.BootParams != "" {
			sb.WriteString(" " + image.BootParams)
		}
		sb.WriteString(autoinstallParams(image, serverAddr, hostname, enrollToken))
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("initrd %s/initrd\n", baseURL))
		sb.WriteString("boot\n")

	case "iso":
		// Boot from ISO via memdisk or sanboot (depending on iPXE capability).
		sb.WriteString(fmt.Sprintf("sanboot %s/image\n", baseURL))

	default:
		return "", fmt.Errorf("unsupported image type for iPXE: %s", image.ImageType)
	}

	return sb.String(), nil
}

// autoinstallParams generates kernel parameters for automated installation.
func autoinstallParams(image db.SandgorgonImage, serverAddr, hostname, enrollToken string) string {
	preseedURL := fmt.Sprintf("%s/sandgorgon/preseed/%s?hostname=%s", serverAddr, image.ID, hostname)
	if enrollToken != "" {
		preseedURL += "&token=" + enrollToken
	}

	switch image.PreseedType {
	case "preseed":
		return fmt.Sprintf(" auto=true priority=critical url=%s hostname=%s", preseedURL, hostname)
	case "autoinstall":
		return fmt.Sprintf(" autoinstall ds=nocloud-net;s=%s/ hostname=%s", preseedURL, hostname)
	case "kickstart":
		return fmt.Sprintf(" inst.ks=%s hostname=%s", preseedURL, hostname)
	default:
		return ""
	}
}

// GeneratePreseedConfig generates an automated installation config for the given image type.
func GeneratePreseedConfig(image db.SandgorgonImage, serverAddr, hostname, enrollToken string) (string, error) {
	switch image.PreseedType {
	case "preseed":
		return renderTemplate(debianPreseedTmpl, preseedData{
			Hostname:    hostname,
			ServerAddr:  serverAddr,
			EnrollToken: enrollToken,
		})
	case "autoinstall":
		return renderTemplate(autoinstallTmpl, preseedData{
			Hostname:    hostname,
			ServerAddr:  serverAddr,
			EnrollToken: enrollToken,
		})
	case "kickstart":
		return renderTemplate(kickstartTmpl, preseedData{
			Hostname:    hostname,
			ServerAddr:  serverAddr,
			EnrollToken: enrollToken,
		})
	default:
		return "", fmt.Errorf("no preseed template for type: %s", image.PreseedType)
	}
}

type preseedData struct {
	Hostname    string
	ServerAddr  string
	EnrollToken string
}

func renderTemplate(tmpl *template.Template, data any) (string, error) {
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// Debian preseed template — minimal, installs SSH and Vigo agent.
var debianPreseedTmpl = template.Must(template.New("preseed").Parse(`# Sandgorgon Debian/Ubuntu preseed
d-i debian-installer/locale string en_US
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/get_hostname string {{.Hostname}}
d-i mirror/http/proxy string
d-i clock-setup/utc boolean true
d-i time/zone string UTC
d-i partman-auto/method string regular
d-i partman-auto/choose_recipe select atomic
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true
d-i passwd/root-login boolean false
d-i pkgsel/include string openssh-server curl
d-i grub-installer/only_debian boolean true
d-i finish-install/reboot_in_progress note

# Vigo agent enrollment
d-i preseed/late_command string \
  in-target bash -c 'curl -sSfk {{.ServerAddr}}/bootstrap{{if .EnrollToken}}?token={{.EnrollToken}}{{end}} | bash'
`))

// Ubuntu autoinstall template (cloud-init based).
var autoinstallTmpl = template.Must(template.New("autoinstall").Parse(`#cloud-config
autoinstall:
  version: 1
  identity:
    hostname: {{.Hostname}}
    password: "$6$disabled"
  ssh:
    install-server: true
  late-commands:
    - curtin in-target -- bash -c 'curl -sSfk {{.ServerAddr}}/bootstrap{{if .EnrollToken}}?token={{.EnrollToken}}{{end}} | bash'
`))

// RHEL/Rocky kickstart template.
var kickstartTmpl = template.Must(template.New("kickstart").Parse(`# Sandgorgon Kickstart
lang en_US.UTF-8
keyboard us
timezone UTC --utc
network --bootproto=dhcp --hostname={{.Hostname}}
rootpw --lock
user --name=admin --lock
clearpart --all --initlabel
autopart
bootloader --append="crashkernel=auto"

%packages
@^minimal-environment
openssh-server
curl
%end

%post
curl -sSfk {{.ServerAddr}}/bootstrap{{if .EnrollToken}}?token={{.EnrollToken}}{{end}} | bash
%end

reboot
`))
