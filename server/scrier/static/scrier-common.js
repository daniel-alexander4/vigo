// Shared helpers for the scrier session pages. Loaded before the
// protocol-specific script; exposes one global, ScrierPage.
//
// Page parameters ride data-* attributes on <body> (html/template escapes
// them), so no inline <script> is needed to hand values to the page.
(function() {
    var body = document.body;
    var display = document.getElementById('scrier-display');
    var statusEl = document.getElementById('scrier-status');

    function setStatus(msg) {
        statusEl.className = '';
        statusEl.textContent = '';
        var sp = document.createElement('span');
        sp.className = 'spinner';
        statusEl.appendChild(sp);
        statusEl.appendChild(document.createTextNode(msg));
    }

    function setError(msg) {
        statusEl.className = 'error';
        statusEl.textContent = msg;
    }

    function waitingMsg() {
        return 'Waiting for agent — may take up to ' + body.dataset.connectWait + '...';
    }

    // startSetup runs phase 1: create the session, poll until ready, then
    // hand the session ID to onReady.
    function startSetup(onReady) {
        setStatus(waitingMsg());
        fetch('/api/v1/scrier/connect', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
                envoy_id: body.dataset.envoyId,
                protocol: body.dataset.protocol,
                port: parseInt(body.dataset.port, 10),
                width: display.clientWidth,
                height: display.clientHeight
            })
        })
        .then(function(resp) { return resp.json(); })
        .then(function(data) {
            if (data.error) { setError('Setup failed: ' + data.error); return; }
            pollStatus(data.session_id, onReady);
        })
        .catch(function(err) { setError('Setup failed: ' + err.message); });
    }

    function pollStatus(sessionID, onReady) {
        var interval = setInterval(function() {
            fetch('/api/v1/scrier/sessions/' + sessionID + '/status')
            .then(function(resp) { return resp.json(); })
            .then(function(data) {
                switch (data.status) {
                    case 'waiting': setStatus(waitingMsg()); break;
                    case 'connecting': setStatus('Agent connected, setting up session...'); break;
                    case 'ready': clearInterval(interval); onReady(sessionID); break;
                    case 'error':
                        clearInterval(interval);
                        setError('Setup failed: ' + (data.error || 'unknown error'));
                        break;
                    default: clearInterval(interval); break;
                }
            })
            .catch(function() {});
        }, 2000);
    }

    function wsURL(sessionID) {
        var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        return proto + '//' + location.host + '/ws/scrier/' + sessionID;
    }

    function showDisconnected(msg) {
        display.textContent = '';
        statusEl.textContent = msg;
        statusEl.className = '';
        var btn = document.createElement('button');
        btn.textContent = 'Reconnect';
        btn.className = 'scrier-reconnect';
        btn.addEventListener('click', function() { location.reload(); });
        var wrap = document.createElement('div');
        wrap.className = 'scrier-ended';
        wrap.appendChild(statusEl);
        wrap.appendChild(document.createElement('br'));
        wrap.appendChild(btn);
        display.appendChild(wrap);
    }

    // bind wires a toolbar button (data-action="name") to a handler.
    function bind(actions) {
        document.querySelectorAll('[data-action]').forEach(function(el) {
            var fn = actions[el.dataset.action];
            if (fn) { el.addEventListener('click', fn); }
        });
    }

    window.ScrierPage = {
        display: display,
        statusEl: statusEl,
        protocol: body.dataset.protocol,
        setStatus: setStatus,
        setError: setError,
        startSetup: startSetup,
        wsURL: wsURL,
        showDisconnected: showDisconnected,
        bind: bind
    };
})();
