
# Sandgorgon Configuration

## Phase 1 Status

In Phase 1, Sandgorgon requires no configuration in `server.yaml`. All settings are passed via CLI flags or the REST API. The subsystem activates automatically when vigosrv starts -- there is nothing to enable.

BMC credentials are passed per-command (`--bmc-user`, `--bmc-pass`) or per-asset at commission time.

## Future: server.yaml Section

Phase 2 and beyond will introduce a `sandgorgon:` section in `server.yaml` for fleet-wide defaults:

```yaml
# Not yet implemented -- planned for Phase 2+
sandgorgon:
  bmc:
    default_username: "admin"
    default_password: "secret:sandgorgon/bmc/default"
    timeout: 30s
    protocol: redfish           # redfish (default) or ipmi
  decommission:
    default_method: purge       # clear, purge, or destroy
    require_technician: true    # require --technician on decommission
  provisioning:                 # Phase 3
    pxe:
      tftp_root: /srv/tftp
      http_root: /srv/pxe
    images:
      directory: /srv/vigo/images
    dhcp:
      integration: kea          # kea, isc, dnsmasq, or none
```

All secret values use the standard `secret:` prefix and are resolved through the configured secrets provider.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
