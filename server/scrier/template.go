// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package scrier

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
        #scrier-display .xterm { height: 100%; }
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
        #scrier-osk button {
            background: #45475a; color: #cdd6f4; border: none;
            padding: 6px 10px; margin: 3px; border-radius: 4px;
            cursor: pointer; font-size: 12px;
        }
        #scrier-osk button:hover { background: #585b70; }
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
            <button onclick="toggleKeyboard()" title="On-screen keyboard for special keys">Keyboard</button>
            <button onclick="window.close()">Close Tab</button>
            <button class="disconnect" onclick="disconnect()">Disconnect</button>
        </div>
    </div>
    <div id="scrier-display">
        <div id="scrier-status"><span class="spinner"></span>Connecting to {{.Hostname}}...</div>
    </div>
    <div id="scrier-osk" style="display:none; position:fixed; bottom:0; left:0; right:0; background:#181825; border-top:1px solid #313244; z-index:100; padding:4px 8px; text-align:center;"></div>

    <script src="/static/js/xterm.min.js?v={{.Version}}"></script>
    <script src="/static/js/xterm-addon-fit.min.js?v={{.Version}}"></script>
    <script>
    (function() {
        var display = document.getElementById('scrier-display');
        var statusEl = document.getElementById('scrier-status');
        var envoyID = '{{.EnvoyID}}';
        var protocol = '{{.Protocol}}';
        var port = {{.Port}};
        var ws = null;
        var term = null;

        function setStatus(msg) {
            statusEl.className = '';
            statusEl.innerHTML = '<span class="spinner"></span>' + msg;
        }

        function setError(msg) {
            statusEl.className = 'error';
            statusEl.textContent = msg;
        }

        // ── Phase 1: Setup via HTTP ──────────────────────────────────────

        function startSetup() {
            setStatus('Waiting for agent...');

            fetch('/api/v1/scrier/connect', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({envoy_id: envoyID, protocol: protocol, port: port, width: display.clientWidth, height: display.clientHeight})
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
                            clearInterval(interval);
                            break;
                    }
                })
                .catch(function() {});
            }, 2000);
        }

        // ── Phase 2: xterm.js + WebSocket relay ─────────────────────────

        function startRelay(sessionID) {
            setStatus('Starting terminal...');

            display.innerHTML = '';

            term = new Terminal({
                cursorBlink: true,
                fontFamily: "'SF Mono', 'Fira Code', 'Fira Mono', Menlo, Consolas, monospace",
                fontSize: 14,
                theme: {
                    background: '#1e1e2e',
                    foreground: '#cdd6f4',
                    cursor: '#f5e0dc',
                    cursorAccent: '#1e1e2e',
                    selectionBackground: '#585b70',
                    black: '#45475a',
                    red: '#f38ba8',
                    green: '#a6e3a1',
                    yellow: '#f9e2af',
                    blue: '#89b4fa',
                    magenta: '#f5c2e7',
                    cyan: '#94e2d5',
                    white: '#bac2de',
                    brightBlack: '#585b70',
                    brightRed: '#f38ba8',
                    brightGreen: '#a6e3a1',
                    brightYellow: '#f9e2af',
                    brightBlue: '#89b4fa',
                    brightMagenta: '#f5c2e7',
                    brightCyan: '#94e2d5',
                    brightWhite: '#a6adc8'
                }
            });

            var fitAddon = new FitAddon.FitAddon();
            term.loadAddon(fitAddon);
            term.open(display);
            fitAddon.fit();

            var wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            var wsURL = wsProto + '//' + location.host + '/ws/scrier/' + sessionID;
            ws = new WebSocket(wsURL);
            ws.binaryType = 'arraybuffer';

            ws.onopen = function() {
                ws.send(JSON.stringify({type: 'resize', cols: term.cols, rows: term.rows}));
                term.focus();
            };

            ws.onmessage = function(e) {
                if (e.data instanceof ArrayBuffer) {
                    term.write(new Uint8Array(e.data));
                }
            };

            ws.onclose = function() {
                showDisconnected('Connection closed.');
            };

            ws.onerror = function() {
                setError('Connection error');
            };

            // Terminal input → WebSocket.
            term.onData(function(data) {
                if (ws && ws.readyState === WebSocket.OPEN) {
                    ws.send(new TextEncoder().encode(data));
                }
            });

            // Resize handling with debounce.
            var resizeTimeout;
            function sendResize() {
                clearTimeout(resizeTimeout);
                resizeTimeout = setTimeout(function() {
                    fitAddon.fit();
                    if (ws && ws.readyState === WebSocket.OPEN) {
                        ws.send(JSON.stringify({type: 'resize', cols: term.cols, rows: term.rows}));
                    }
                }, 150);
            }
            window.addEventListener('resize', sendResize);
        }

        // ── On-screen keyboard ───────────────────────────────────────────

        var oskVisible = false;
        var oskBuilt = false;
        window.toggleKeyboard = function() {
            var container = document.getElementById('scrier-osk');
            oskVisible = !oskVisible;
            container.style.display = oskVisible ? 'flex' : 'none';
            container.style.flexWrap = 'wrap';
            container.style.justifyContent = 'center';
            if (oskVisible && !oskBuilt) {
                oskBuilt = true;
                var keys = [
                    {label: 'Esc', seq: '\x1b'},
                    {label: 'Tab', seq: '\t'},
                    {label: 'Ctrl+C', seq: '\x03'},
                    {label: 'Ctrl+D', seq: '\x04'},
                    {label: 'Ctrl+Z', seq: '\x1a'},
                    {label: 'Ctrl+L', seq: '\x0c'},
                    {label: 'Ctrl+A', seq: '\x01'},
                    {label: 'Ctrl+E', seq: '\x05'},
                    {label: '\u2191', seq: '\x1b[A'},
                    {label: '\u2193', seq: '\x1b[B'},
                    {label: '\u2190', seq: '\x1b[D'},
                    {label: '\u2192', seq: '\x1b[C'}
                ];
                keys.forEach(function(k) {
                    var btn = document.createElement('button');
                    btn.textContent = k.label;
                    btn.onclick = function() {
                        if (ws && ws.readyState === WebSocket.OPEN) {
                            ws.send(new TextEncoder().encode(k.seq));
                        }
                        if (term) term.focus();
                    };
                    container.appendChild(btn);
                });
            }
            if (term) term.focus();
        };

        // ── Disconnect / reconnect ───────────────────────────────────────

        window.disconnect = function() {
            if (ws) ws.close();
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

        startSetup();
    })();
    </script>
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
                body: JSON.stringify({envoy_id: envoyID, protocol: protocol, port: port, width: display.clientWidth, height: display.clientHeight})
            })
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                if (data.error) { setError('Setup failed: ' + data.error); return; }
                pollStatus(data.session_id);
            })
            .catch(function(err) { setError('Setup failed: ' + err.message); });
        }

        function pollStatus(sessionID) {
            var interval = setInterval(function() {
                fetch('/api/v1/scrier/sessions/' + sessionID + '/status')
                .then(function(resp) { return resp.json(); })
                .then(function(data) {
                    switch(data.status) {
                        case 'waiting': setStatus('Waiting for agent...'); break;
                        case 'connecting': setStatus('Agent connected, setting up session...'); break;
                        case 'ready': clearInterval(interval); startRelay(sessionID); break;
                        case 'error': clearInterval(interval); setError('Setup failed: ' + (data.error || 'unknown error')); break;
                        default: clearInterval(interval); break;
                    }
                })
                .catch(function() {});
            }, 2000);
        }

        function startRelay(sessionID) {
            setStatus('Starting session...');
            var wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
            var wsURL = wsProto + '//' + location.host + '/ws/scrier/' + sessionID;
            var tunnel = new Guacamole.WebSocketTunnel(wsURL);
            guac = new Guacamole.Client(tunnel);

            tunnel.onerror = function(status) { setError('Connection error: ' + (status.message || status.code || 'unknown')); };
            guac.onerror = function(status) { setError('Session error: ' + (status.message || status.code || 'unknown')); };

            guac.onargv = function(stream) { stream.onblob = function() {}; stream.onend = function() {}; };
            guac.getDisplay().onresize = function() { applyScale(); };

            guac.onstatechange = function(state) {
                switch(state) {
                    case 1: setStatus('Connecting...'); break;
                    case 2: setStatus('Waiting for server...'); break;
                    case 3:
                        display.innerHTML = '';
                        display.appendChild(guac.getDisplay().getElement());
                        setupInput();
                        setTimeout(applyScale, 500);
                        break;
                    case 4: showDisconnected('Connection closed by server.'); break;
                    case 5: showDisconnected('Disconnected.'); break;
                }
            };

            guac.connect();

            var origHandler = tunnel.oninstruction;
            tunnel.oninstruction = function(opcode, args) {
                if (origHandler) origHandler(opcode, args);
                if (opcode === 'blob') { guac.sendAck(parseInt(args[0]), 'OK', 0x0000); }
            };
        }

        function setupInput() {
            var el = guac.getDisplay().getElement();
            var mouse = new Guacamole.Mouse(el);
            mouse.onEach(['mousedown', 'mouseup', 'mousemove'], function(e) {
                // Adjust mouse coordinates for display scaling so the cursor
                // position in the browser matches the remote desktop position.
                var guacDisplay = guac.getDisplay();
                var scale = guacDisplay.getScale();
                var adjusted = new Guacamole.Mouse.State(
                    e.state.x / scale,
                    e.state.y / scale,
                    e.state.left,
                    e.state.middle,
                    e.state.right,
                    e.state.up,
                    e.state.down
                );
                guac.sendMouseState(adjusted);
            });
            var keyboard = new Guacamole.Keyboard(document);
            keyboard.onkeydown = function(keysym) { guac.sendKeyEvent(1, keysym); };
            keyboard.onkeyup = function(keysym) { guac.sendKeyEvent(0, keysym); };
            function resize() {
                // RDP supports server-side resize; VNC does not.
                if (protocol === 'rdp') {
                    var w = display.clientWidth, h = display.clientHeight;
                    guac.sendSize(w, h);
                }
                applyScale();
            }
            window.addEventListener('resize', resize);
            if (protocol === 'rdp') { resize(); }
            // VNC: apply initial scale after first frame arrives.
            setTimeout(applyScale, 300);
        }

        window.pasteClipboard = function() {
            if (!guac || !navigator.clipboard || !navigator.clipboard.readText) return;
            navigator.clipboard.readText().then(function(text) {
                if (!text) return;
                var stream = guac.createClipboardStream('text/plain');
                var writer = new Guacamole.StringWriter(stream);
                writer.sendText(text); writer.sendEnd();
            }).catch(function() {});
        };

        window.uploadFile = function() {
            if (!guac) return;
            var input = document.createElement('input'); input.type = 'file';
            input.onchange = function() {
                var file = input.files[0]; if (!file) return;
                var stream = guac.createFileStream(file.type || 'application/octet-stream', file.name);
                var writer = new Guacamole.BlobWriter(stream);
                writer.oncomplete = function() { stream.sendEnd(); }; writer.send(file);
            };
            input.click();
        };

        var fitScale = true;
        window.toggleScale = function() {
            fitScale = !fitScale;
            document.getElementById('scale-btn').textContent = fitScale ? 'Fit' : '1:1';
            applyScale();
        };
        function applyScale() {
            if (!guac) return;
            var guacDisplay = guac.getDisplay(); if (!guacDisplay) return;
            if (fitScale) {
                var s = Math.min(display.clientWidth / guacDisplay.getWidth(), display.clientHeight / guacDisplay.getHeight(), 1);
                guacDisplay.scale(s);
                display.style.overflow = 'hidden';
                display.style.justifyContent = 'center';
                display.style.alignItems = 'center';
            } else {
                guacDisplay.scale(1);
                display.style.overflow = 'auto';
                display.style.justifyContent = 'flex-start';
                display.style.alignItems = 'flex-start';
            }
        }

        var oskVisible = false, osk = null;
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

        window.disconnect = function() { if (guac) guac.disconnect(); showDisconnected('Disconnected by user.'); };

        function showDisconnected(msg) {
            display.innerHTML = '';
            statusEl.textContent = msg; statusEl.className = '';
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

        startSetup();
    })();
    </script>
</body>
</html>`
