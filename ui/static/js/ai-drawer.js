(function() {
    // The script tag rides the body swap on every boosted nav, so without a
    // module-level guard the IIFE re-runs and each run creates a fresh closure.
    // window.toggleAIChat / aiDrawerSuggest / aiDrawerSubmit get reassigned to
    // the latest closure's functions, but listeners registered earlier still
    // reference the original closure's state — closures alias and state goes
    // incoherent. Pin to the first execution; later loads no-op. The
    // afterSettle listener registered below handles re-init for boosted navs.
    if (window.__vigoAIDrawerLoaded) return;
    window.__vigoAIDrawerLoaded = true;

    var STORAGE_KEY = 'vigo_ai_drawer';
    var busy = false;
    var open = false;
    var history = [];
    // Server-rendered welcome HTML, captured once from base.html so close+reopen
    // can restore it without a duplicated copy in this file. The first init()
    // after page load reads from the freshly-rendered DOM; subsequent boosted
    // navs see the cached value.
    var welcomeHTML = '';

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

        // Cache the server-rendered welcome the first time we see it, so close
        // can reinject it without this file carrying a duplicate string copy.
        if (!welcomeHTML) {
            var w = document.querySelector('.ai-panel__welcome');
            if (w) welcomeHTML = w.outerHTML;
        }

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
            if (messages && welcomeHTML) {
                messages.innerHTML = welcomeHTML;
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
        contentEl.appendChild(statusEl);
        scroll();
        saveState();

        var controller = new AbortController();
        var timeout = setTimeout(function() { controller.abort(); }, 120000);

        // Detect page context from data-ai-* attributes.
        var aiCtx = {};
        var aiEl = document.querySelector('[data-ai-page]');
        if (aiEl) {
            aiCtx.page = aiEl.getAttribute('data-ai-page') || '';
            if (aiEl.getAttribute('data-ai-envoy')) aiCtx.envoy = aiEl.getAttribute('data-ai-envoy');
            if (aiEl.getAttribute('data-ai-run')) aiCtx.run_id = aiEl.getAttribute('data-ai-run');
            if (aiEl.getAttribute('data-ai-framework')) aiCtx.framework = aiEl.getAttribute('data-ai-framework');
        }

        var docsEl = document.querySelector('[data-docs-page]');
        var pageTitle = document.querySelector('h1');
        var pageName = docsEl ? docsEl.getAttribute('data-docs-page')
                     : (aiCtx.envoy || (pageTitle ? pageTitle.textContent.trim() : ''));

        if (docsEl) {
            statusEl.textContent = 'Adding "' + pageName + '" into context\u2026';
            statusEl.setAttribute('data-docs-status', '1');
        } else if (pageName) {
            statusEl.textContent = 'Adding ' + pageName + ' context\u2026';
        } else {
            statusEl.textContent = 'Thinking\u2026';
        }
        scroll();

        // Use setTimeout to yield to the browser so the status message renders
        // before we do the synchronous context building and network call.
        setTimeout(function() {
        if (!statusEl.parentNode) return; // guard against drawer closed during yield
        var docsCtx = '';
        if (docsEl) {
            var docTitle = docsEl.getAttribute('data-docs-page') || '';
            var pagePath = docsEl.getAttribute('data-docs-path') || '';
            var docsBody = document.querySelector('.docs-body');
            var pageContent = docsBody ? docsBody.innerText : '';

            var docsIndex = '';
            if (typeof window._vigoDocsPages !== 'undefined' && window._vigoDocsPages.length) {
                docsIndex = 'DOCUMENTATION INDEX (use to find related topics):\n';
                for (var di = 0; di < window._vigoDocsPages.length; di++) {
                    var dp = window._vigoDocsPages[di];
                    docsIndex += '- ' + dp.t + ' (/docs/' + dp.p + ')\n';
                }
                docsIndex += '\n';
            }

            docsCtx = docsIndex +
                'CURRENT DOCUMENT: "' + docTitle + '" (/docs/' + pagePath + ')\n' +
                'Answer the question primarily in the context of this document. ' +
                'Reference other documents from the index above only if relevant.\n\n' +
                pageContent + '\n\n';

            statusEl.textContent = 'Asking about "' + docTitle + '"\u2026';
            scroll();
        } else {
            statusEl.textContent = 'Thinking\u2026';
        }

        fetch('/ui/ask', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({question: docsCtx + q, context: aiCtx, history: history.slice(0, -1)}),
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
        }, 0); // end setTimeout — yields to browser so status message renders

        return false;
    };

    function handleEv(ev, contentEl, statusEl) {
        switch (ev.type) {
        case 'status':
            if (ev.tools && ev.tools.length) {
                statusEl.removeAttribute('data-docs-status');
                statusEl.textContent = 'Querying: ' + ev.tools.join(', ') + '...';
            } else if (ev.status === 'processing') {
                // Don't overwrite docs-specific status (e.g. 'Processing "Architecture"...')
                if (!statusEl.hasAttribute('data-docs-status')) {
                    statusEl.textContent = 'Processing...';
                }
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
