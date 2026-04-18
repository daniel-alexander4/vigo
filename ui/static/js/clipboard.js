// Clipboard helper — wires any button with [data-copy-target="<id>"] to copy
// the text content of the element with that id, showing a brief "Copied!"
// acknowledgement. Uses document-level event delegation so it works across
// HTMX boost navigation without needing to re-bind.
(function () {
    'use strict';

    document.addEventListener('click', function (ev) {
        const btn = ev.target.closest('[data-copy-target]');
        if (!btn) return;
        const id = btn.getAttribute('data-copy-target');
        const src = document.getElementById(id);
        if (!src) return;
        const text = src.textContent;
        const reset = function () {
            btn.textContent = btn.dataset.originalLabel || 'Copy';
        };
        if (!btn.dataset.originalLabel) {
            btn.dataset.originalLabel = btn.textContent;
        }
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).then(function () {
                btn.textContent = 'Copied';
                setTimeout(reset, 1500);
            }).catch(function () {
                btn.textContent = 'Copy failed';
                setTimeout(reset, 1500);
            });
        } else {
            // Fallback for older browsers: use a hidden textarea.
            const ta = document.createElement('textarea');
            ta.value = text;
            ta.style.position = 'fixed';
            ta.style.top = '-1000px';
            document.body.appendChild(ta);
            ta.select();
            try {
                document.execCommand('copy');
                btn.textContent = 'Copied';
            } catch (e) {
                btn.textContent = 'Copy failed';
            }
            document.body.removeChild(ta);
            setTimeout(reset, 1500);
        }
    });
})();
