(function() {
    var drawer = document.getElementById('ai-drawer');
    var messages = document.getElementById('ai-drawer-messages');
    var input = document.getElementById('ai-drawer-input');
    var sendBtn = document.getElementById('ai-drawer-send');
    var fab = document.getElementById('ai-fab');
    var fabIcon = document.getElementById('ai-fab-icon');
    var fabClose = document.getElementById('ai-fab-close');
    if (!drawer || !messages || !input) return;

    var STORAGE_KEY = 'hobnob_ai_drawer';
    var busy = false;
    var open = false;

    // Restore state from sessionStorage.
    var history = [];
    try {
        var saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY));
        if (saved && saved.history && saved.history.length) {
            history = saved.history;
            // Rebuild message bubbles from saved history.
            var welcome = messages.querySelector('.ai-drawer__welcome');
            if (welcome) welcome.remove();
            for (var i = 0; i < history.length; i++) {
                var msg = history[i];
                var el = addMsg(msg.role === 'user' ? 'user' : 'assistant', '');
                var contentEl = el.querySelector('.chat-msg-content');
                if (msg.role === 'user') {
                    contentEl.textContent = msg.content;
                } else {
                    contentEl.innerHTML = msg.html || msg.content;
                }
            }
            // Restore open state.
            if (saved.open) {
                open = true;
                drawer.classList.add('ai-drawer--open');
                fab.classList.add('ai-fab--open');
                fabIcon.style.display = 'none';
                fabClose.style.display = '';
            }
        }
    } catch(e) {}

    function saveState() {
        try {
            sessionStorage.setItem(STORAGE_KEY, JSON.stringify({
                history: history,
                open: open
            }));
        } catch(e) {}
    }

    window.toggleAIChat = function() {
        open = !open;
        drawer.classList.toggle('ai-drawer--open', open);
        fab.classList.toggle('ai-fab--open', open);
        fabIcon.style.display = open ? 'none' : '';
        fabClose.style.display = open ? '' : 'none';
        if (open) input.focus();
        saveState();
    };

    window.aiDrawerSuggest = function(btn) {
        var q = btn.getAttribute('data-question') || btn.textContent;
        input.value = q;
        aiDrawerSubmit(new Event('submit'));
    };

    window.aiDrawerSubmit = function(e) {
        e.preventDefault();
        var q = input.value.trim();
        if (!q || busy) return false;

        var welcome = messages.querySelector('.ai-drawer__welcome');
        if (welcome) welcome.remove();

        addMsg('user', q);
        history.push({role: 'user', content: q});
        input.value = '';
        input.style.height = 'auto';
        busy = true;
        sendBtn.disabled = true;

        var assistantEl = addMsg('assistant', '');
        var contentEl = assistantEl.querySelector('.chat-msg-content');
        var statusEl = document.createElement('div');
        statusEl.className = 'chat-status';
        statusEl.textContent = 'Thinking...';
        contentEl.appendChild(statusEl);
        scroll();
        saveState();

        var controller = new AbortController();
        var timeout = setTimeout(function() { controller.abort(); }, 120000);

        fetch('/ui/ask', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({question: q, history: history.slice(0, -1)}),
            signal: controller.signal
        }).then(function(resp) {
            if (!resp.ok) throw new Error('Request failed: ' + resp.status);
            var reader = resp.body.getReader();
            var decoder = new TextDecoder();
            var buffer = '';

            function read() {
                return reader.read().then(function(result) {
                    if (result.done) { clearTimeout(timeout); finish(); return; }
                    buffer += decoder.decode(result.value, {stream: true});
                    var lines = buffer.split('\n');
                    buffer = lines.pop();
                    for (var i = 0; i < lines.length; i++) {
                        if (lines[i].indexOf('data: ') === 0) {
                            try {
                                var ev = JSON.parse(lines[i].slice(6));
                                handleEv(ev, contentEl, statusEl);
                            } catch(e) {}
                        }
                    }
                    return read();
                });
            }
            return read();
        }).catch(function(err) {
            clearTimeout(timeout);
            statusEl.remove();
            var msg = err.name === 'AbortError'
                ? 'Request timed out. Check that the AI provider is running.'
                : err.message;
            contentEl.innerHTML = '<p class="chat-error">Error: ' + msg + '</p>';
            finish();
        });

        return false;
    };

    function handleEv(ev, contentEl, statusEl) {
        switch (ev.type) {
        case 'status':
            if (ev.tools && ev.tools.length) {
                statusEl.textContent = 'Querying: ' + ev.tools.join(', ') + '...';
            } else if (ev.status === 'processing') {
                statusEl.textContent = 'Processing...';
            }
            break;
        case 'delta':
            statusEl.remove();
            contentEl.innerHTML = ev.html;
            scroll();
            break;
        case 'done':
            if (ev.raw_answer) {
                history.push({role: 'assistant', content: ev.raw_answer, html: contentEl.innerHTML});
                saveState();
            }
            if (ev.tools_used && ev.tools_used.length) {
                var meta = document.createElement('div');
                meta.className = 'chat-meta';
                meta.textContent = ev.tools_used.length + ' tool' +
                    (ev.tools_used.length > 1 ? 's' : '') +
                    ' used \u00b7 ' + (ev.tokens_used || 0) + ' tokens';
                contentEl.parentElement.appendChild(meta);
            }
            break;
        case 'error':
            statusEl.remove();
            contentEl.innerHTML = '<p class="chat-error">Error: ' + ev.text + '</p>';
            break;
        }
    }

    function finish() {
        busy = false;
        sendBtn.disabled = false;
        input.focus();
        scroll();
        saveState();
    }

    function addMsg(role, text) {
        var el = document.createElement('div');
        el.className = 'chat-msg chat-msg--' + role;
        var bubble = document.createElement('div');
        bubble.className = 'chat-bubble chat-bubble--' + role;
        if (role === 'user') {
            var label = document.createElement('div');
            label.className = 'chat-msg-label';
            label.textContent = 'You';
            bubble.appendChild(label);
        }
        var content = document.createElement('div');
        content.className = 'chat-msg-content';
        if (text) content.textContent = text;
        bubble.appendChild(content);
        el.appendChild(bubble);
        messages.appendChild(el);
        scroll();
        return el;
    }

    function scroll() {
        messages.scrollTop = messages.scrollHeight;
    }
})();
