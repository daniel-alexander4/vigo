# Troubleshooting

Common issues encountered when using Sandgorgon, with diagnostic steps and solutions.

## BMC Unreachable

**Symptom:** `vigocli sandgorgon commission` or `vigocli sandgorgon decommission` fails with a connection error when attempting to reach the BMC.

**Diagnostic steps:**

1. **Network connectivity:** Verify the Vigo server can reach the BMC address. BMCs are typically on an out-of-band management network. Confirm the server has a route to that network.
   ```
   ping <bmc-address>
   curl -k https://<bmc-address>/redfish/v1/
   ```

2. **Firewall rules:** BMC Redfish uses HTTPS, typically on port 443. Ensure no firewall between the Vigo server and the BMC is blocking this port.

3. **Redfish enabled:** Not all BMCs have Redfish enabled by default. Check the BMC's web interface or IPMI settings to confirm Redfish is turned on.
   - Dell iDRAC: Redfish is enabled by default on iDRAC 8+
   - HPE iLO: Redfish is enabled by default on iLO 4+
   - Supermicro: Redfish may need to be enabled in BMC settings

4. **Correct port:** While 443 is the default, some BMCs use non-standard ports (8443, 8080). Check the BMC documentation or web interface URL.

5. **TLS issues:** BMCs almost universally use self-signed certificates. Sandgorgon accepts self-signed certs by default. If you have configured strict TLS verification, this may be the cause.

## Authentication Failed

**Symptom:** Redfish connection succeeds but returns 401 Unauthorized or 403 Forbidden.

**Diagnostic steps:**

1. **Verify credentials:** Check that `sandgorgon.bmc_username` and `sandgorgon.bmc_password` in the server configuration are correct. Test manually:
   ```
   curl -k -u <username>:<password> https://<bmc-address>/redfish/v1/Systems/1/
   ```

2. **Separate Redfish users:** Some BMCs maintain separate user databases for the web interface, IPMI, and Redfish. A user that works for web login may not have Redfish access. Check the BMC's user management settings.

3. **Account lockout:** Repeated failed attempts may trigger account lockout on the BMC. Check the BMC's security settings for lockout thresholds and reset if needed.

4. **Per-asset credentials:** If different BMCs use different credentials, configure per-asset overrides rather than relying on the global `sandgorgon.bmc_username`/`sandgorgon.bmc_password` settings.

5. **Privilege level:** The Redfish user must have Administrator privileges to perform SecureErase and other write operations. Read-only users can enumerate hardware but cannot dispatch sanitization.

## No Drives Found

**Symptom:** Decommission workflow reports "no drives found" even though the machine has storage.

**Diagnostic steps:**

1. **Check Redfish Storage subsystem:** Not all BMC implementations populate the Storage subsystem completely. Query it manually:
   ```
   curl -k -u <user>:<pass> https://<bmc-address>/redfish/v1/Systems/1/Storage/
   ```
   Then for each controller:
   ```
   curl -k -u <user>:<pass> https://<bmc-address>/redfish/v1/Systems/1/Storage/<controller>/Drives/
   ```

2. **Vendor differences:** Redfish implementations vary by vendor. Some BMCs only expose drives attached to RAID controllers in the Storage resource, not drives on the motherboard's SATA/NVMe controller. Check if the drives are exposed under a different controller ID.

3. **BMC firmware version:** Older BMC firmware may have incomplete Redfish implementations. Update the BMC firmware to the latest version and retry.

4. **NVMe drives on CPU lanes:** NVMe drives connected directly to CPU PCIe lanes (not through a RAID controller or backplane with BMC management) may not appear in the Redfish Storage subsystem at all. These drives require OS-level sanitization (future Phase: boot into wipe image).

5. **Virtual drives vs. physical drives:** Some BMCs expose RAID virtual drives but not the underlying physical drives. Check both the `Drives` and `Volumes` collections under each Storage controller.

## SecureErase Failed

**Symptom:** The sanitization command was sent but the drive reports a failure, or the Redfish task fails.

**Diagnostic steps:**

1. **Drive does not support SecureErase:** Not all drives support firmware-level erase. Check the drive's capabilities in the Redfish Drive resource:
   ```
   curl -k -u <user>:<pass> https://<bmc-address>/redfish/v1/Systems/1/Storage/<ctrl>/Drives/<drive>/
   ```
   Look for the `Operations` array. If SecureErase is not listed, the drive does not support it.

2. **Try a lower method:** If the default method fails, try `--method clear` which uses a simpler erase command. Some drives support Clear but not Purge.

3. **Drive is frozen:** SATA drives have a "security frozen" state that prevents Secure Erase from being issued. This is typically set by the BIOS during boot. Some BMCs can unfreeze the drive; others require a power cycle.

4. **Drive is part of a RAID array:** Drives in an active RAID array may not accept direct SecureErase commands. The RAID controller intercepts the command. Delete the RAID virtual drive first, then retry.

5. **Timeout:** Large HDDs can take many hours to erase. The default timeout scales with capacity. If the drive is still working (check task progress in Redfish), wait for completion. If the task is genuinely stuck, a BMC reboot may be required (coordinate with the operator -- Sandgorgon will not reboot BMCs automatically).

## Certificate Shows "unverifiable"

**Symptom:** The NIST 800-88 certificate for a drive shows `verification_result: unverifiable`.

**This is expected behavior for SATA drives.** It is not a failure or an error.

ATA Secure Erase provides no completion proof. There is no SMART attribute or ATA log page that records whether a Secure Erase completed successfully. The drive simply returns to a ready state after the command finishes.

The certificate honestly states: "Command issued, drive responsive post-erase." This meets NIST 800-88 Clear requirements. The "unverifiable" status means Sandgorgon cannot independently confirm the erase completed -- it is a statement of the protocol's limitation, not a deficiency in Sandgorgon.

See [nist-800-88.md](nist-800-88.md) for a full explanation of verification honesty per media type.

**If your organization requires provable sanitization for all drives:**
- Use NVMe drives (Sanitize Log provides provable completion)
- Use Self-Encrypting Drives with TCG Opal (MEK rotation is provable)
- Use physical destruction for drives that do not support provable firmware erase

## Import Skipped All Rows

**Symptom:** `vigocli sandgorgon import --source csv --file assets.csv` reports that all rows were skipped.

**Diagnostic steps:**

1. **Check column names:** CSV column names must match exactly. Required columns:
   - `bmc_address` (required)
   - `hostname` (optional, used for envoy matching)
   - `serial_number` (optional)
   - `asset_tag` (optional)
   - `rack_location` (optional)

   Column names are case-sensitive. `BMC_Address` or `bmc-address` will not match. Use exactly `bmc_address`.

2. **Check for BOM:** Some spreadsheet applications (Excel) add a UTF-8 BOM (byte order mark) to CSV files. This can cause the first column header to be misread. Open the file in a text editor and check for invisible characters before the first column name.

3. **Empty bmc_address:** Rows where the `bmc_address` column is empty are skipped. Every row must have a BMC address.

4. **Delimiter:** The CSV parser expects comma-separated values. Tab-separated or semicolon-separated files will not parse correctly. If your data is tab-separated, convert it to comma-separated first.

5. **Quoting:** Values containing commas must be quoted. For example: `"rack-14, U22"` not `rack-14, U22`.

6. **Duplicate detection:** If all rows match existing assets (by serial number or BMC address), they are treated as updates, not inserts. The import will report "updated" rather than "created." If all rows are duplicates and nothing changed, the import may report zero actions.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
