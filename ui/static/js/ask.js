(function() {
    var messages = document.getElementById('chat-messages');
    var input = document.getElementById('chat-input');
    var sendBtn = document.getElementById('chat-send');
    if (!messages || !input || !sendBtn) return;

    var history = [];
    var busy = false;

    window.askSuggestion = function(btn) {
        // Support data-question attribute or fall back to textContent.
        var q = btn.getAttribute('data-question') || btn.textContent;
        input.value = q;
        handleSubmit(new Event('submit'));
    };

    window.handleSubmit = function(e) {
        e.preventDefault();
        var q = input.value.trim();
        if (!q || busy) return false;

        // Remove welcome on first message.
        var welcome = messages.querySelector('.chat-welcome');
        if (welcome) welcome.remove();

        addMessage('user', q);
        history.push({role: 'user', content: q});
        input.value = '';
        autoResize();
        busy = true;
        sendBtn.disabled = true;

        var assistantEl = addMessage('assistant', '');
        var contentEl = assistantEl.querySelector('.chat-msg-content');
        var statusEl = document.createElement('div');
        statusEl.className = 'chat-status';
        statusEl.textContent = 'Thinking...';
        contentEl.appendChild(statusEl);
        scrollToBottom();

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
                        var line = lines[i];
                        if (line.indexOf('data: ') === 0) {
                            try {
                                var ev = JSON.parse(line.slice(6));
                                handleEvent(ev, contentEl, statusEl);
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
                ? 'Request timed out. Check that the AI provider is running and reachable.'
                : err.message;
            contentEl.className = 'chat-msg-content chat-error';
            contentEl.textContent = 'Error: ' + msg;
            finish();
        });

        return false;
    };

    function handleEvent(ev, contentEl, statusEl) {
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
            scrollToBottom();
            break;
        case 'done':
            if (ev.raw_answer) {
                history.push({role: 'assistant', content: ev.raw_answer});
            }
            if (ev.tools_used && ev.tools_used.length) {
                var meta = document.createElement('div');
                meta.className = 'chat-meta';
                meta.textContent = ev.tools_used.length + ' tool' + (ev.tools_used.length > 1 ? 's' : '') +
                    ' used \u00b7 ' + (ev.tokens_used || 0) + ' tokens';
                contentEl.parentElement.appendChild(meta);
            }
            finish();
            break;
        case 'error':
            statusEl.remove();
            contentEl.className = 'chat-msg-content chat-error';
            contentEl.textContent = 'Error: ' + ev.text;
            finish();
            break;
        }
    }

    function finish() {
        busy = false;
        sendBtn.disabled = false;
        input.focus();
        scrollToBottom();
    }

    function addMessage(role, text) {
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
        scrollToBottom();
        return el;
    }

    function scrollToBottom() {
        messages.scrollTop = messages.scrollHeight;
    }

    function autoResize() {
        input.style.height = 'auto';
        input.style.height = Math.min(input.scrollHeight, 120) + 'px';
    }
    input.addEventListener('input', autoResize);
})();
