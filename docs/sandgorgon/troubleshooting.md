
# Troubleshooting

Common issues when working with Sandgorgon bare metal lifecycle management.

## BMC Unreachable

**Symptom:** `BMC unreachable at <address>` during commission or decommission.

**Causes:**
- BMC is on a separate management VLAN that vigosrv cannot reach
- Firewall blocking HTTPS (port 443) to BMC
- Redfish not enabled on the BMC (some BMCs ship with Redfish disabled)
- Wrong IP address or hostname

**Fixes:**
- Verify network reachability: `curl -k https://<bmc-address>/redfish/v1/`
- Check BMC management interface settings — enable Redfish if needed
- Ensure vigosrv can reach the BMC network (may need routing or a management VLAN bridge)

## Authentication Failed

**Symptom:** HTTP 401 from BMC during Redfish operations.

**Causes:**
- Incorrect BMC username or password
- Some BMCs have separate Redfish user accounts distinct from the web UI login
- Default credentials may have been changed

**Fixes:**
- Verify credentials by logging into the BMC web UI directly
- Check if the BMC has a dedicated Redfish user account
- Use `--bmc-user` and `--bmc-pass` flags during commission, or update via the API

## No Drives Found

**Symptom:** Decommission completes with zero drives sanitized.

**Causes:**
- BMC's Redfish Storage subsystem not populated (varies by vendor)
- Drives connected to a RAID controller that does not expose individual drives via Redfish
- Some older BMC firmware versions have incomplete Redfish implementations

**Fixes:**
- Check the Redfish storage endpoint manually: `curl -k -u user:pass https://<bmc>/redfish/v1/Systems/1/Storage`
- Update BMC firmware to the latest version
- For RAID controllers, check `/redfish/v1/Systems/1/Storage/{ControllerID}/Drives`

## SecureErase Failed

**Symptom:** Drive sanitization reports `failed` status.

**Causes:**
- Drive does not support SecureErase via Redfish
- Drive is in a frozen security state (common with SATA drives)
- RAID controller intercepting the command

**Fixes:**
- Try a different method: `vigogo sandgorgon decommission <host> --method clear`
- Check drive capabilities in the Redfish drive resource (Operations field)
- For frozen SATA drives, a power cycle may be needed before erase

## Certificate Shows "unverifiable"

**This is expected for SATA drives**, not a failure.

SATA Secure Erase has no completion proof in SMART data. The certificate honestly states that the command was issued and the drive is responsive afterward. This follows Vigo's honest compliance principle.

For provable sanitization, use NVMe drives (Sanitize Log) or SEDs (TCG Opal status). See [NIST 800-88 Compliance](nist-800-88.md).

## Import Skipped All Rows

**Symptom:** `vigogo sandgorgon import` reports all rows skipped.

**Causes:**
- CSV column names do not match expected names (case-sensitive)
- All BMC addresses already exist in the database (duplicates skipped)

**Fixes:**
- Verify CSV header row matches: `hostname,bmc_address,serial_number,asset_tag,rack_location`
- The `bmc_address` column is required
- Check existing assets: `vigogo sandgorgon status`

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
