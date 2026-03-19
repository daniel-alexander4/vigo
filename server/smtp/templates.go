// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package smtp

import "text/template"

// emailTemplates contains templates for each event type.
// Each template outputs: first line = subject, remaining lines = body.
var emailTemplates = template.Must(template.New("").Parse(`
{{- define "envoy.enrolled" -}}
[Vigo] Envoy enrolled: {{ .Envoy.Hostname }}
Envoy {{ .Envoy.Hostname }} has been enrolled.

  ID:       {{ .Envoy.ID }}
  Hostname: {{ .Envoy.Hostname }}
  IP:       {{ .Envoy.IP }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "run.success" -}}
[Vigo] Run succeeded: {{ .Envoy.Hostname }}
Convergence run completed successfully on {{ .Envoy.Hostname }}.

  Run ID:   {{ .Run.ID }}
  Status:   {{ .Run.Status }}
  Changed:  {{ .Run.Changed }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "run.failure" -}}
[Vigo] Run FAILED: {{ .Envoy.Hostname }}
Convergence run failed on {{ .Envoy.Hostname }}.

  Run ID:   {{ .Run.ID }}
  Status:   {{ .Run.Status }}
  Changed:  {{ .Run.Changed }}
  Failed:   {{ .Run.Failed }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "drift.detected" -}}
[Vigo] Drift detected: {{ .Envoy.Hostname }}
Configuration drift was detected on {{ .Envoy.Hostname }}.

  Run ID:   {{ .Run.ID }}
  Changed:  {{ .Run.Changed }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "secret.rotated" -}}
[Vigo] Secret rotated: {{ index .Details "path" }}
A secret was rotated and affected envoys have been queued for re-convergence.

  Path:     {{ index .Details "path" }}
  Affected: {{ index .Details "affected" }} envoy(s)
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "config.reload.failure" -}}
[Vigo] Config reload failed
Configuration reload failed. The previous config remains active.

  Error: {{ index .Details "error" }}
  Time:  {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "envoy.stale" -}}
[Vigo] Envoy stale: {{ .Envoy.Hostname }}
Envoy {{ .Envoy.Hostname }} has not checked in within the expected interval.

  ID:       {{ .Envoy.ID }}
  Hostname: {{ .Envoy.Hostname }}
  Last seen: {{ index .Details "last_seen" }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "compliance.threshold" -}}
[Vigo] Compliance below threshold: {{ index .Details "compliant_pct" }}%
Compliance has dropped below the configured threshold.

  Compliant: {{ index .Details "compliant" }}/{{ index .Details "total" }} ({{ index .Details "compliant_pct" }}%)
  Threshold: {{ index .Details "threshold" }}%
  Time:      {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "digest" -}}
[Vigo] Digest: {{ .Total }} events
{{ .Total }} event(s) in this digest period.
{{ range .Types }}
  {{ . }}: {{ index $.Counts . }}
{{- end }}

Details:
{{ range .Events }}
  [{{ .Timestamp.Format "15:04:05" }}] {{ .Name }}{{ if .Envoy }} — {{ .Envoy.Hostname }}{{ end }}
{{- end }}
{{- end -}}

{{- define "default" -}}
[Vigo] {{ .Name }}
Event: {{ .Name }}
Time:  {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{ if .Envoy }}
Envoy: {{ .Envoy.Hostname }} ({{ .Envoy.ID }})
{{- end }}
{{ if .Run }}
Run: {{ .Run.ID }} — {{ .Run.Status }}
{{- end }}
{{- end -}}
`))
