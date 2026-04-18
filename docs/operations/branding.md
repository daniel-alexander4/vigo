
# Branding

Vigo's web UI and exported PDF reports can be white-labeled with an organization name, logo, favicon, and a forced UI theme. Branding values are sourced from the `branding:` section of `server.yaml` and applied across the UI header, sidebar, generated PDFs, and browser tabs.

## Config

```yaml
branding:
  org_name: "Acme Corp"
  logo_path: "/srv/vigo/branding/logo.svg"
  favicon_path: "/srv/vigo/branding/favicon.ico"
  theme: "auto"
```

All fields are optional. Unset fields fall back to the embedded Vigo defaults.

| Field | Purpose |
|-------|---------|
| `org_name` | Shown in the UI header, sidebar footer, and in the cover block of exported PDF reports. |
| `logo_path` | Absolute filesystem path to a custom logo image (SVG, PNG, JPG, GIF, WebP). ~120×40 works well in the sidebar. Inlined as a data URI into exported PDFs so attestations travel self-contained. |
| `favicon_path` | Absolute path to a custom favicon (`.ico`, `.svg`, or `.png`). |
| `theme` | `light`, `dark`, or `auto` (follows the viewer's `prefers-color-scheme`). Default is `light`. |

## Logo resolution order

When the insurance attestation PDF or any other logo-bearing document is generated, Vigo resolves the logo in this order and uses the first match:

1. `branding.logo_path` from `server.yaml`, if set and readable.
2. `/srv/vigo/branding/logo.svg` — conventional operator drop-in path. Drop a file here and it works without editing `server.yaml`.
3. Embedded Vigo default logo, compiled into the binary.

Files over 2 MiB are rejected (logos should be a few KB to ~100 KB; a larger file either isn't a logo or bloats every PDF render).

## MSP / per-customer branding

In a spanner deployment, each spoke has its own `server.yaml` and therefore its own branding. An MSP operator running multiple customer spokes can give each customer their own logo, org name, and theme without any multi-tenancy gymnastics — every spoke is already isolated by design.

## Forcing a theme

Users can always override the branded theme from the UI theme switcher. `theme: dark` (or `light`) sets the default for unauthenticated users and new sessions but doesn't prevent a user from flipping to the other mode. Set `theme: auto` if you want the UI to respect each viewer's OS preference.

## Where the logo renders

- UI sidebar header — the navigation area carries the `logo_path` image alongside the org name.
- UI page title — prefixed with the org name in browser tabs.
- Cyber-insurance attestation PDF cover block — logo inlined as a data URI, org name below.
- Future PDF exports (compliance reports, audit summaries) use the same cover block.

## Related

- [Server-config > branding](../getting-started/server-config.md#branding) — config reference
- [Cyber-insurance attestation](../concepts/risk-scoring.md#where-scores-appear) — uses the branded logo on the cover
