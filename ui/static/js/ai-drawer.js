(function() {
    var STORAGE_KEY = 'vigo_ai_drawer';
    var busy = false;
    var open = false;
    var history = [];

    // Restore persistent state from sessionStorage.
    try {
        var saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY));
        if (saved) {
            if (saved.history && saved.history.length) history = saved.history;
            if (saved.open) open = true;
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

    // Rebuild message bubbles from saved history into the current DOM.
    function restoreMessages() {
        var messages = document.getElementById('ai-drawer-messages');
        if (!messages) return;

        if (history.length === 0) return;

        var welcome = messages.querySelector('.ai-panel__welcome');
        if (welcome) welcome.remove();
        for (var i = 0; i < history.length; i++) {
            var msg = history[i];
            var el = addMsg(msg.role === 'user' ? 'user' : 'assistant', '');
            var contentEl = el.querySelector('.chat-msg-content');
            if (msg.role === 'user') {
                contentEl.textContent = msg.content;
            } else {
                // Server-rendered HTML — sanitized by goldmark (raw HTML stripped, see render.go).
                contentEl.innerHTML = msg.html || msg.content;
            }
        }
    }

    // Apply open/closed state to current DOM elements.
    function applyOpenState() {
        var panel = document.getElementById('ai-panel');
        var fab = document.getElementById('ai-fab');
        if (!panel || !fab) return;

        panel.classList.toggle('ai-panel--open', open);
        fab.classList.toggle('ai-toggle--open', open);
    }

    // Initialize or re-initialize after HTMX navigation.
    var initialized = false;
    function init() {
        var panel = document.getElementById('ai-panel');
        if (!panel) return;

        // Only restore messages once — not on every HTMX fragment swap.
        if (!initialized) {
            restoreMessages();
            initialized = true;
        }
        applyOpenState();
    }

    window.toggleAIChat = function() {
        open = !open;
        applyOpenState();
        if (open) {
            var input = document.getElementById('ai-drawer-input');
            if (input) input.focus();
        } else {
            // Clear history when drawer closes — next open starts fresh.
            history = [];
            var messages = document.getElementById('ai-drawer-messages');
            if (messages) {
                messages.innerHTML = '<div class="ai-panel__welcome">' +
                    '<p>Ask anything about your fleet, compliance, or configuration.</p>' +
                    '<div class="ai-panel__suggestions">' +
                    '<button class="chat-suggestion" onclick="aiDrawerSuggest(this)">How many envoys?</button>' +
                    '<button class="chat-suggestion" onclick="aiDrawerSuggest(this)" data-question="Which envoys are failing and why?">Failing envoys?</button>' +
                    '<button class="chat-suggestion" onclick="aiDrawerSuggest(this)" data-question="Show the fleet compliance summary with breakdown by status.">Compliance summary</button>' +
                    '</div></div>';
            }
        }
        saveState();
    };

    window.aiDrawerSuggest = function(btn) {
        var q = btn.getAttribute('data-question') || btn.textContent;
        // Close security detail drawer if open, so AI chat has full attention.
        if (typeof closeSecDrawer === 'function') {
            try { closeSecDrawer(); } catch(e) {}
        }
        if (!open) toggleAIChat();
        var input = document.getElementById('ai-drawer-input');
        if (input) input.value = q;
        aiDrawerSubmit(new Event('submit'));
    };

    window.aiDrawerSubmit = function(e) {
        e.preventDefault();
        var input = document.getElementById('ai-drawer-input');
        var sendBtn = document.getElementById('ai-drawer-send');
        var messages = document.getElementById('ai-drawer-messages');
        if (!input || !messages) return false;

        var q = input.value.trim();
        if (!q || busy) return false;

        var welcome = messages.querySelector('.ai-panel__welcome');
        if (welcome) welcome.remove();

        addMsg('user', q);
        history.push({role: 'user', content: q});
        input.value = '';
        input.style.height = 'auto';
        busy = true;
        if (sendBtn) sendBtn.disabled = true;

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

        // Include docs page content as context if on a documentation page.
        var docsCtx = '';
        var docsEl = document.querySelector('[data-docs-page]');
        if (docsEl) {
            var docsBody = document.querySelector('.docs-body');
            if (docsBody) {
                docsCtx = 'Context: Documentation page "' + docsEl.getAttribute('data-docs-page') + '":\n' + docsBody.innerText.substring(0, 2000) + '\n\n';
            }
        }

        fetch('/ui/ask', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({question: docsCtx + q, history: history.slice(0, -1)}),
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
            contentEl.className = 'chat-msg-content chat-error';
            contentEl.textContent = 'Error: ' + msg;
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
            // Server-rendered HTML — sanitized by goldmark (raw HTML stripped, see render.go).
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
            contentEl.className = 'chat-msg-content chat-error';
            contentEl.textContent = 'Error: ' + ev.text;
            break;
        }
    }

    function finish() {
        busy = false;
        var sendBtn = document.getElementById('ai-drawer-send');
        var input = document.getElementById('ai-drawer-input');
        if (sendBtn) sendBtn.disabled = false;
        if (input) input.focus();
        scroll();
        saveState();
    }

    function addMsg(role, text) {
        var messages = document.getElementById('ai-drawer-messages');
        if (!messages) return document.createElement('div');

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
        var messages = document.getElementById('ai-drawer-messages');
        if (messages) messages.scrollTop = messages.scrollHeight;
    }

    // Initialize now and re-initialize after boosted page navigations only.
    init();
    document.body.addEventListener('htmx:afterSettle', function(evt) {
        // Only re-init on full page swaps (boosted navigation), not fragment updates.
        if (evt.detail && evt.detail.target === document.body) {
            initialized = false;
            init();
        } else {
            // Just ensure open/closed state is correct.
            applyOpenState();
        }
    });
})();
