// Scrier SSH session page: xterm.js over the /ws/scrier/{id} raw PTY relay.
(function() {
    var P = window.ScrierPage;
    var display = P.display;
    var ws = null;
    var term = null;

    function startRelay(sessionID) {
        P.setStatus('Starting terminal...');
        display.textContent = '';

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

        ws = new WebSocket(P.wsURL(sessionID));
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
        ws.onclose = function() { P.showDisconnected('Connection closed.'); };
        ws.onerror = function() { P.setError('Connection error'); };

        term.onData(function(data) {
            if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(new TextEncoder().encode(data));
            }
        });

        var resizeTimeout;
        window.addEventListener('resize', function() {
            clearTimeout(resizeTimeout);
            resizeTimeout = setTimeout(function() {
                fitAddon.fit();
                if (ws && ws.readyState === WebSocket.OPEN) {
                    ws.send(JSON.stringify({type: 'resize', cols: term.cols, rows: term.rows}));
                }
            }, 150);
        });
    }

    var oskBuilt = false;
    function toggleKeyboard() {
        var container = document.getElementById('scrier-osk');
        container.classList.toggle('open');
        if (container.classList.contains('open') && !oskBuilt) {
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
                {label: '↑', seq: '\x1b[A'},
                {label: '↓', seq: '\x1b[B'},
                {label: '←', seq: '\x1b[D'},
                {label: '→', seq: '\x1b[C'}
            ];
            keys.forEach(function(k) {
                var btn = document.createElement('button');
                btn.textContent = k.label;
                btn.addEventListener('click', function() {
                    if (ws && ws.readyState === WebSocket.OPEN) {
                        ws.send(new TextEncoder().encode(k.seq));
                    }
                    if (term) term.focus();
                });
                container.appendChild(btn);
            });
        }
        if (term) term.focus();
    }

    P.bind({
        keyboard: toggleKeyboard,
        close: function() { window.close(); },
        disconnect: function() {
            if (ws) ws.close();
            P.showDisconnected('Disconnected by user.');
        }
    });

    P.startSetup(startRelay);
})();
