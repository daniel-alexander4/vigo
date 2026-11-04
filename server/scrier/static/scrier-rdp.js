// Scrier RDP/VNC session page: Guacamole client over the /ws/scrier/{id} relay.
(function() {
    var P = window.ScrierPage;
    var display = P.display;
    var statusEl = P.statusEl;
    var protocol = P.protocol;
    var guac = null;
    var fitScale = true;

    function startRelay(sessionID) {
        P.setStatus('Starting session...');
        var tunnel = new Guacamole.WebSocketTunnel(P.wsURL(sessionID));
        guac = new Guacamole.Client(tunnel);

        tunnel.onerror = function(status) { P.setError('Connection error: ' + (status.message || status.code || 'unknown')); };
        guac.onerror = function(status) { P.setError('Session error: ' + (status.message || status.code || 'unknown')); };

        guac.onargv = function(stream) { stream.onblob = function() {}; stream.onend = function() {}; };
        guac.getDisplay().onresize = function() { applyScale(); };

        guac.onstatechange = function(state) {
            switch (state) {
                case 1: P.setStatus('Connecting...'); break;
                case 2: P.setStatus('Waiting for server...'); break;
                case 3:
                    display.textContent = '';
                    display.appendChild(guac.getDisplay().getElement());
                    setupInput();
                    setTimeout(applyScale, 500);
                    break;
                case 4: P.showDisconnected('Connection closed by server.'); break;
                case 5: P.showDisconnected('Disconnected.'); break;
            }
        };

        guac.connect();

        var origHandler = tunnel.oninstruction;
        tunnel.oninstruction = function(opcode, args) {
            if (origHandler) origHandler(opcode, args);
            if (opcode === 'blob') { guac.sendAck(parseInt(args[0], 10), 'OK', 0x0000); }
        };
    }

    function setupInput() {
        var el = guac.getDisplay().getElement();
        // Guacamole.Mouse uses offsetLeft/offsetTop, which ignore scroll
        // offsets of parent containers: when the display is larger than the
        // viewport in 1:1 mode, coordinates drift. Track the raw DOM mouse
        // position via getBoundingClientRect() and use that instead.
        var lastMouseX = 0, lastMouseY = 0;
        function track(e) {
            var rect = el.getBoundingClientRect();
            lastMouseX = e.clientX - rect.left;
            lastMouseY = e.clientY - rect.top;
        }
        el.addEventListener('mousemove', track);
        el.addEventListener('mousedown', track);
        el.addEventListener('mouseup', track);
        // Forward wheel events to the remote desktop instead of scrolling.
        el.addEventListener('wheel', function(e) { e.preventDefault(); track(e); }, {passive: false});
        var mouse = new Guacamole.Mouse(el);
        mouse.onEach(['mousedown', 'mouseup', 'mousemove'], function(e) {
            var scale = guac.getDisplay().getScale();
            guac.sendMouseState(new Guacamole.Mouse.State(
                lastMouseX / scale, lastMouseY / scale,
                e.state.left, e.state.middle, e.state.right,
                e.state.up, e.state.down
            ));
        });
        var keyboard = new Guacamole.Keyboard(document);
        keyboard.onkeydown = function(keysym) { guac.sendKeyEvent(1, keysym); };
        keyboard.onkeyup = function(keysym) { guac.sendKeyEvent(0, keysym); };
        function resize() {
            // RDP supports server-side resize; VNC does not. Only resize in
            // fit mode — in 1:1 mode the remote resolution stays fixed so
            // scrollbars work.
            if (protocol === 'rdp' && fitScale) {
                guac.sendSize(display.clientWidth, display.clientHeight);
            }
            applyScale();
        }
        window.addEventListener('resize', resize);
        if (protocol === 'rdp') { resize(); }
        // VNC: apply the initial scale after the first frame arrives.
        setTimeout(applyScale, 300);
    }

    function pasteClipboard() {
        if (!guac) { P.setError('Paste: no active session'); return; }
        if (!navigator.clipboard || !navigator.clipboard.readText) {
            P.setError('Paste: browser blocks clipboard read (needs HTTPS and user permission)');
            return;
        }
        navigator.clipboard.readText().then(function(text) {
            if (!text) { P.setError('Paste: local clipboard is empty'); return; }
            try {
                var stream = guac.createClipboardStream('text/plain');
                var writer = new Guacamole.StringWriter(stream);
                writer.sendText(text); writer.sendEnd();
                // The remote clipboard is seeded; the operator still presses
                // Ctrl-V on the remote.
                statusEl.className = '';
                statusEl.textContent = 'Clipboard sent — press Ctrl-V on the remote';
                setTimeout(function() { statusEl.textContent = ''; }, 3000);
            } catch (streamErr) {
                P.setError('Paste: ' + (streamErr && streamErr.message ? streamErr.message : 'failed to send clipboard'));
            }
        }).catch(function(err) {
            // Only expose err.message, never clipboard content.
            P.setError('Paste blocked: ' + (err && err.message ? err.message : 'permission denied'));
        });
    }

    function uploadFile() {
        if (!guac) return;
        var input = document.createElement('input');
        input.type = 'file';
        input.addEventListener('change', function() {
            var file = input.files[0];
            if (!file) return;
            var stream = guac.createFileStream(file.type || 'application/octet-stream', file.name);
            var writer = new Guacamole.BlobWriter(stream);
            writer.oncomplete = function() { stream.sendEnd(); };
            writer.send(file);
        });
        input.click();
    }

    function toggleScale() {
        fitScale = !fitScale;
        document.getElementById('scale-btn').textContent = fitScale ? 'Fit' : '1:1';
        if (fitScale && protocol === 'rdp' && guac) {
            // Resize RDP to the container so fit mode fills the viewport.
            guac.sendSize(display.clientWidth, display.clientHeight);
        }
        applyScale();
    }

    function applyScale() {
        if (!guac) return;
        var guacDisplay = guac.getDisplay();
        if (!guacDisplay) return;
        if (fitScale) {
            var s = Math.min(display.clientWidth / guacDisplay.getWidth(), display.clientHeight / guacDisplay.getHeight(), 1);
            guacDisplay.scale(s);
            display.classList.remove('scale-1to1');
        } else {
            guacDisplay.scale(1);
            display.classList.add('scale-1to1');
        }
    }

    var osk = null;
    function toggleKeyboard() {
        var container = document.getElementById('scrier-osk');
        container.classList.toggle('open');
        if (container.classList.contains('open') && !osk) {
            osk = new Guacamole.OnScreenKeyboard('/static/osk/en-us-qwerty.json');
            container.appendChild(osk.getElement());
            osk.onkeydown = function(keysym) { if (guac) guac.sendKeyEvent(1, keysym); };
            osk.onkeyup = function(keysym) { if (guac) guac.sendKeyEvent(0, keysym); };
            osk.resize(container.clientWidth);
        }
    }

    P.bind({
        upload: uploadFile,
        paste: pasteClipboard,
        keyboard: toggleKeyboard,
        scale: toggleScale,
        close: function() { window.close(); },
        disconnect: function() {
            if (guac) guac.disconnect();
            P.showDisconnected('Disconnected by user.');
        }
    });

    P.startSetup(startRelay);
})();
