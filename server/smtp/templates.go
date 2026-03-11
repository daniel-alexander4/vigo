package smtp

import "text/template"

// emailTemplates contains templates for each event type.
// Each template outputs: first line = subject, remaining lines = body.
var emailTemplates = template.Must(template.New("").Parse(`
{{- define "thrall.enrolled" -}}
[Hobnob] Thrall enrolled: {{ .Thrall.Hostname }}
Thrall {{ .Thrall.Hostname }} has been enrolled.

  ID:       {{ .Thrall.ID }}
  Hostname: {{ .Thrall.Hostname }}
  IP:       {{ .Thrall.IP }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "run.success" -}}
[Hobnob] Run succeeded: {{ .Thrall.Hostname }}
Convergence run completed successfully on {{ .Thrall.Hostname }}.

  Run ID:   {{ .Run.ID }}
  Status:   {{ .Run.Status }}
  Changed:  {{ .Run.Changed }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "run.failure" -}}
[Hobnob] Run FAILED: {{ .Thrall.Hostname }}
Convergence run failed on {{ .Thrall.Hostname }}.

  Run ID:   {{ .Run.ID }}
  Status:   {{ .Run.Status }}
  Changed:  {{ .Run.Changed }}
  Failed:   {{ .Run.Failed }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "drift.detected" -}}
[Hobnob] Drift detected: {{ .Thrall.Hostname }}
Configuration drift was detected on {{ .Thrall.Hostname }}.

  Run ID:   {{ .Run.ID }}
  Changed:  {{ .Run.Changed }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "secret.rotated" -}}
[Hobnob] Secret rotated: {{ index .Details "path" }}
A secret was rotated and affected thralls have been queued for re-convergence.

  Path:     {{ index .Details "path" }}
  Affected: {{ index .Details "affected" }} thrall(s)
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "config.reload.failure" -}}
[Hobnob] Config reload failed
Configuration reload failed. The previous config remains active.

  Error: {{ index .Details "error" }}
  Time:  {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "thrall.stale" -}}
[Hobnob] Thrall stale: {{ .Thrall.Hostname }}
Thrall {{ .Thrall.Hostname }} has not checked in within the expected interval.

  ID:       {{ .Thrall.ID }}
  Hostname: {{ .Thrall.Hostname }}
  Last seen: {{ index .Details "last_seen" }}
  Time:     {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{- end -}}

{{- define "default" -}}
[Hobnob] {{ .Name }}
Event: {{ .Name }}
Time:  {{ .Timestamp.Format "2006-01-02 15:04:05 UTC" }}
{{ if .Thrall }}
Thrall: {{ .Thrall.Hostname }} ({{ .Thrall.ID }})
{{- end }}
{{ if .Run }}
Run: {{ .Run.ID }} — {{ .Run.Status }}
{{- end }}
{{- end -}}
`))
