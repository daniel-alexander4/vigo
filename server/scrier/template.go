// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

// scrierSessionHTML is the full-page template for a Scrier remote access session.
// Two-phase connection:
//   1. POST /api/v1/scrier/connect → poll status until ready
//   2. Open WebSocket to /ws/scrier/{session_id} → immediate relay
const scrierSessionHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>{{.Hostname}} — Vigo Scrier</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        html, body { height: 100%; overflow: hidden; background: #1e1e2e; color: #cdd6f4; font-family: system-ui, sans-serif; }
        .scrier-toolbar {
            display: flex; align-items: center; justify-content: space-between;
            padding: 6px 16px; background: #181825; border-bottom: 1px solid #313244;
            font-size: 13px; height: 36px;
        }
        .scrier-toolbar .info { display: flex; align-items: center; gap: 12px; }
        .scrier-toolbar .badge {
            background: #313244; padding: 2px 8px; border-radius: 4px;
            font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px;
        }
        .scrier-toolbar button {
            background: #45475a; border: none; color: #cdd6f4; padding: 4px 12px;
            border-radius: 4px; cursor: pointer; font-size: 12px;
        }
        .scrier-toolbar button:hover { background: #585b70; }
        .scrier-toolbar .disconnect { background: #f38ba8; color: #1e1e2e; }
        .scrier-toolbar .disconnect:hover { background: #eba0ac; }
        #scrier-display {
            width: 100%; height: calc(100vh - 36px);
            display: flex; align-items: center; justify-content: center;
            overflow: hidden;
        }
        #scrier-status {
            color: #a6adc8; font-size: 14px; text-align: center;
        }
        #scrier-status.error { color: #f38ba8; }
        .spinner {
            display: inline-block; width: 16px; height: 16px;
            border: 2px solid #585b70; border-top-color: #89b4fa;
            border-radius: 50%; animation: spin 0.8s linear infinite;
            vertical-align: middle; margin-right: 8px;
        }
        @keyframes spin { to { transform: rotate(360deg); } }
    </style>
</head>
<body>
    <div class="scrier-toolbar">
        <div class="info">
            <strong>{{.Hostname}}</strong>
            <span class="badge">{{.Protocol}}</span>
            <span class="badge">:{{.Port}}</span>
        </div>
        <div style="display:flex; gap:6px; align-items:center;">
            <button onclick="uploadFile()" title="Upload file to remote">Upload</button>
            <button onclick="pasteClipboard()" title="Paste local clipboard to remote">Paste</button>
            <button onclick="toggleKeyboard()" title="On-screen keyboard for special keys">Keyboard</button>
            <button onclick="toggleScale()" id="scale-btn" title="Toggle display scaling">Fit</button>
            <button onclick="window.close()">Close Tab</button>
            <button class="disconnect" onclick="disconnect()">Disconnect</button>
        </div>
    </div>
    <div id="scrier-display">
        <div id="scrier-status"><span class="spinner"></span>Connecting to {{.Hostname}}...</div>
    </div>
    <div id="scrier-osk" style="display:none; position:fixed; bottom:0; left:0; right:0; background:#181825; border-top:1px solid #313244; z-index:100;"></div>

    <script src="/static/js/guacamole.min.js?v={{.Version}}"></script>
    <script>
    (function() {
        var display = document.getElementById('scrier-display');
        var statusEl = document.getElementById('scrier-status');
        var envoyID = '{{.EnvoyID}}';
        var protocol = '{{.Protocol}}';
        var port = {{.Port}};
        var guac = null;

        // ── Phase 1: Setup via HTTP ──────────────────────────────────────
        // POST to create session, then poll until ready.

        function setStatus(msg) {
            statusEl.className = '';
            statusEl.innerHTML = '<span class="spinner"></span>' + msg;
        }

        function setError(msg) {
            statusEl.className = 'error';
            statusEl.textContent = msg;
        }

        function startSetup() {
            setStatus('Waiting for agent...');

            fetch('/api/v1/scrier/connect', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({envoy_id: envoyID, protocol: protocol, port: port})
            })
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                if (data.error) {
                    setError('Setup failed: ' + data.error);
                    return;
                }
                pollStatus(data.session_id);
            })
            .catch(function(err) {
                setError('Setup failed: ' + err.message);
            });
        }

        function pollStatus(sessionID) {
            var interval = setInterval(function() {
                fetch('/api/v1/scrier/sessions/' + sessionID + '/status')
                .then(function(resp) { return resp.json(); })
                .then(function(data) {
                    switch(data.status) {
                        case 'waiting':
                            setStatus('Waiting for agent...');
                            break;
                        case 'connecting':
                            setStatus('Agent connected, setting up session...');
                            break;
                        case 'ready':
                            clearInterval(interval);
                            startRelay(sessionID);
                            break;
                        case 'error':
                            clearInterval(interval);
                            setError('Setup failed: ' + (data.error || 'unknown error'));
                            break;
                        default:
                            // relaying or other — stop polling
                            clearInterval(interval);
                            break;
                    }
                })
                .catch(function() {
                    // Network error — keep polling.
                });
            }, 2000);
        }

        // ── Phase 2: WebSocket relay ─────────────────────────────────────
        // Open WebSocket only when session is ready. guacd is already connected.
        // First display data flows immediately.

        function startRelay(sessionID) {
            setStatus('Starting terminal...');

            var wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            var wsURL = wsProto + '//' + location.host + '/ws/scrier/' + sessionID;

            var tunnel = new Guacamole.WebSocketTunnel(wsURL);
            guac = new Guacamole.Client(tunnel);

            tunnel.onerror = function(status) {
                setError('Connection error: ' + (status.message || status.code || 'unknown'));
            };

            guac.onerror = function(status) {
                setError('Session error: ' + (status.message || status.code || 'unknown'));
            };

            guac.onstatechange = function(state) {
                switch(state) {
                    case 1: setStatus('Connecting...'); break;
                    case 2: setStatus('Waiting for server...'); break;
                    case 3:
                        // Connected — replace status with display.
                        display.innerHTML = '';
                        display.appendChild(guac.getDisplay().getElement());
                        setupInput();
                        setTimeout(applyScale, 500);
                        break;
                    case 4:
                        showDisconnected('Connection closed by server.');
                        break;
                    case 5:
                        showDisconnected('Disconnected.');
                        break;
                }
            };

            guac.connect();
        }

        // ── Input handling ───────────────────────────────────────────────

        function setupInput() {
            var el = guac.getDisplay().getElement();

            var mouse = new Guacamole.Mouse(el);
            mouse.onEach(['mousedown', 'mouseup', 'mousemove'], function(e) {
                guac.sendMouseState(e.state);
            });

            var keyboard = new Guacamole.Keyboard(document);
            keyboard.onkeydown = function(keysym) { guac.sendKeyEvent(1, keysym); };
            keyboard.onkeyup = function(keysym) { guac.sendKeyEvent(0, keysym); };

            function resize() {
                var w = display.clientWidth;
                var h = display.clientHeight;
                guac.sendSize(w, h);
                applyScale();
            }
            window.addEventListener('resize', resize);
            resize();
        }

        // ── Clipboard ────────────────────────────────────────────────────

        window.pasteClipboard = function() {
            if (!guac || !navigator.clipboard || !navigator.clipboard.readText) return;
            navigator.clipboard.readText().then(function(text) {
                if (!text) return;
                var stream = guac.createClipboardStream('text/plain');
                var writer = new Guacamole.StringWriter(stream);
                writer.sendText(text);
                writer.sendEnd();
            }).catch(function() {});
        };

        // ── File transfer ────────────────────────────────────────────────

        window.uploadFile = function() {
            if (!guac) return;
            var input = document.createElement('input');
            input.type = 'file';
            input.onchange = function() {
                var file = input.files[0];
                if (!file) return;
                var stream = guac.createFileStream(file.type || 'application/octet-stream', file.name);
                var writer = new Guacamole.BlobWriter(stream);
                writer.oncomplete = function() { stream.sendEnd(); };
                writer.send(file);
            };
            input.click();
        };

        // ── Display scaling ──────────────────────────────────────────────

        var fitScale = true;
        window.toggleScale = function() {
            fitScale = !fitScale;
            document.getElementById('scale-btn').textContent = fitScale ? 'Fit' : '1:1';
            applyScale();
        };
        function applyScale() {
            if (!guac) return;
            var guacDisplay = guac.getDisplay();
            if (!guacDisplay) return;
            if (fitScale) {
                var sw = display.clientWidth / guacDisplay.getWidth();
                var sh = display.clientHeight / guacDisplay.getHeight();
                var s = Math.min(sw, sh, 1);
                guacDisplay.scale(s);
            } else {
                guacDisplay.scale(1);
            }
        }

        // ── On-screen keyboard ───────────────────────────────────────────

        var oskVisible = false;
        var osk = null;
        window.toggleKeyboard = function() {
            var container = document.getElementById('scrier-osk');
            oskVisible = !oskVisible;
            container.style.display = oskVisible ? 'block' : 'none';
            if (oskVisible && !osk) {
                osk = new Guacamole.OnScreenKeyboard('/static/osk/en-us-qwerty.json');
                container.appendChild(osk.getElement());
                osk.onkeydown = function(keysym) { if (guac) guac.sendKeyEvent(1, keysym); };
                osk.onkeyup = function(keysym) { if (guac) guac.sendKeyEvent(0, keysym); };
                osk.resize(container.clientWidth);
            }
        };

        // ── Disconnect / reconnect ───────────────────────────────────────

        window.disconnect = function() {
            if (guac) guac.disconnect();
            showDisconnected('Disconnected by user.');
        };

        function showDisconnected(msg) {
            display.innerHTML = '';
            statusEl.textContent = msg;
            statusEl.className = '';
            var reconnectBtn = document.createElement('button');
            reconnectBtn.textContent = 'Reconnect';
            reconnectBtn.style.cssText = 'margin-top:16px; padding:8px 20px; background:#89b4fa; color:#1e1e2e; border:none; border-radius:4px; cursor:pointer; font-size:14px;';
            reconnectBtn.onclick = function() { location.reload(); };
            var wrap = document.createElement('div');
            wrap.style.cssText = 'text-align:center; padding:40px;';
            wrap.appendChild(statusEl);
            wrap.appendChild(document.createElement('br'));
            wrap.appendChild(reconnectBtn);
            display.appendChild(wrap);
        }

        // ── Start ────────────────────────────────────────────────────────
        startSetup();
    })();
    </script>
</body>
</html>`
