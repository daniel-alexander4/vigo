// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

// scrierSessionHTML is the full-page template for a Scrier remote access session.
// Opens in a new tab — no Vigo navigation chrome, just the remote display.
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
    </style>
</head>
<body>
    <div class="scrier-toolbar">
        <div class="info">
            <strong>{{.Hostname}}</strong>
            <span class="badge">{{.Protocol}}</span>
            <span class="badge">:{{.Port}}</span>
        </div>
        <div>
            <button onclick="window.close()">Close Tab</button>
            <button class="disconnect" onclick="disconnect()">Disconnect</button>
        </div>
    </div>
    <div id="scrier-display">
        <div id="scrier-status">Connecting to {{.Hostname}}...</div>
    </div>

    <script src="/static/js/guacamole.min.js?v={{.Version}}"></script>
    <script>
    (function() {
        var display = document.getElementById('scrier-display');
        var statusEl = document.getElementById('scrier-status');
        var envoyID = '{{.EnvoyID}}';
        var protocol = '{{.Protocol}}';
        var port = '{{.Port}}';

        // Build WebSocket URL.
        var wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        var wsURL = wsProto + '//' + location.host + '/ws/tunnel/' + envoyID + '?protocol=' + protocol + '&port=' + port;

        var tunnel = new Guacamole.WebSocketTunnel(wsURL);
        var guac = new Guacamole.Client(tunnel);

        tunnel.onerror = function(status) {
            statusEl.className = 'error';
            statusEl.textContent = 'Connection error: ' + (status.message || 'unknown');
        };

        guac.onerror = function(status) {
            statusEl.className = 'error';
            statusEl.textContent = 'Session error: ' + (status.message || 'unknown');
        };

        guac.onstatechange = function(state) {
            switch(state) {
                case 1: statusEl.textContent = 'Connecting...'; break;
                case 2: statusEl.textContent = 'Waiting for server...'; break;
                case 3:
                    // Connected — replace status with display.
                    display.innerHTML = '';
                    display.appendChild(guac.getDisplay().getElement());
                    setupInput();
                    break;
                case 4:
                case 5:
                    statusEl.textContent = 'Disconnected.';
                    display.innerHTML = '';
                    display.appendChild(statusEl);
                    break;
            }
        };

        function setupInput() {
            var el = guac.getDisplay().getElement();

            var mouse = new Guacamole.Mouse(el);
            mouse.onEach(['mousedown', 'mouseup', 'mousemove'], function(e) {
                guac.sendMouseState(e.state);
            });

            var keyboard = new Guacamole.Keyboard(document);
            keyboard.onkeydown = function(keysym) { guac.sendKeyEvent(1, keysym); };
            keyboard.onkeyup = function(keysym) { guac.sendKeyEvent(0, keysym); };

            // Auto-resize.
            function resize() {
                var w = display.clientWidth;
                var h = display.clientHeight;
                guac.sendSize(w, h);
            }
            window.addEventListener('resize', resize);
            resize();
        }

        window.disconnect = function() {
            guac.disconnect();
            statusEl.textContent = 'Disconnected.';
            display.innerHTML = '';
            display.appendChild(statusEl);
        };

        // Connect.
        guac.connect();
    })();
    </script>
</body>
</html>`
