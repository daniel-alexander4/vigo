// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package scrier

// The session pages carry no inline <script>, <style>, style= attribute or
// on*= handler: behaviour and styling live in the package's embedded
// static assets (static/, served at /scrier/assets/) so the pages run under
// the locked CSP. Page parameters ride data-* attributes on <body>, which
// html/template escapes. TestSessionTemplatesHaveNoInlineCode guards it.

// scrierSessionSSHHTML is the full-page template for SSH sessions using xterm.js.
// Two-phase connection:
//  1. POST /api/v1/scrier/connect → poll status until ready
//  2. Open WebSocket to /ws/scrier/{session_id} → raw PTY byte relay
const scrierSessionSSHHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Hostname}} — Vigo Scrier</title>
    <link rel="stylesheet" href="/static/css/xterm.css?v={{.Version}}">
    <link rel="stylesheet" href="/scrier/assets/scrier.css?v={{.Version}}">
</head>
<body data-envoy-id="{{.EnvoyID}}" data-protocol="{{.Protocol}}" data-port="{{.Port}}" data-connect-wait="{{.ConnectWait}}">
    <div class="scrier-toolbar">
        <div class="info">
            <strong>{{.Hostname}}</strong>
            <span class="badge">{{.Protocol}}</span>
            <span class="badge">:{{.Port}}</span>
        </div>
        <div class="actions">
            <button type="button" data-action="keyboard" title="On-screen keyboard for special keys">Keyboard</button>
            <button type="button" data-action="close">Close Tab</button>
            <button type="button" class="disconnect" data-action="disconnect">Disconnect</button>
        </div>
    </div>
    <div id="scrier-display">
        <div id="scrier-status"><span class="spinner"></span>Connecting to {{.Hostname}}...</div>
    </div>
    <div id="scrier-osk" class="keys"></div>

    <script src="/static/js/xterm.min.js?v={{.Version}}"></script>
    <script src="/static/js/xterm-addon-fit.min.js?v={{.Version}}"></script>
    <script src="/scrier/assets/scrier-common.js?v={{.Version}}"></script>
    <script src="/scrier/assets/scrier-ssh.js?v={{.Version}}"></script>
</body>
</html>`

// scrierSessionRDPHTML is the full-page template for RDP sessions using Guacamole.
// Two-phase connection:
//  1. POST /api/v1/scrier/connect → poll status until ready
//  2. Open WebSocket to /ws/scrier/{session_id} → Guacamole protocol relay
const scrierSessionRDPHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Hostname}} — Vigo Scrier</title>
    <link rel="stylesheet" href="/scrier/assets/scrier.css?v={{.Version}}">
</head>
<body data-envoy-id="{{.EnvoyID}}" data-protocol="{{.Protocol}}" data-port="{{.Port}}" data-connect-wait="{{.ConnectWait}}">
    <div class="scrier-toolbar">
        <div class="info">
            <strong>{{.Hostname}}</strong>
            <span class="badge">{{.Protocol}}</span>
            <span class="badge">:{{.Port}}</span>
        </div>
        <div class="actions">
            <button type="button" data-action="upload" title="Upload a file — appears on a 'Vigo' drive on the remote (RDP drive redirection)">Upload</button>
            <button type="button" data-action="paste" title="Send local clipboard to remote clipboard — then press Ctrl-V on the remote to complete the paste">Paste</button>
            <button type="button" data-action="keyboard" title="On-screen keyboard for special keys">Keyboard</button>
            <button type="button" data-action="scale" id="scale-btn" title="Toggle display scaling">Fit</button>
            <button type="button" data-action="close">Close Tab</button>
            <button type="button" class="disconnect" data-action="disconnect">Disconnect</button>
        </div>
    </div>
    <div id="scrier-display">
        <div id="scrier-status"><span class="spinner"></span>Connecting to {{.Hostname}}...</div>
    </div>
    <div id="scrier-osk"></div>

    <script src="/static/js/guacamole.min.js?v={{.Version}}"></script>
    <script src="/scrier/assets/scrier-common.js?v={{.Version}}"></script>
    <script src="/scrier/assets/scrier-rdp.js?v={{.Version}}"></script>
</body>
</html>`
