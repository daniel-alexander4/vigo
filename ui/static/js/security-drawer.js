// Security page — drawer state + in-drawer filter UI state only.
//
// All data fetching is server-rendered HTMX (see security.html and
// ui/security_drawer.go::SecurityEnvoyFragment). This file exists because
// the drawer's open/close classes and the severity-filter button state
// are pure client-side UI concerns — HTMX has no opinion on them.
//
// Deliberate non-goals:
//  - No fetch() calls. No JSON→DOM rendering.
//  - No timers. No setInterval.
//  - No dependency on `security_scan_detail` JSON shape.

(function () {
    'use strict';

    // Stat-card panel toggle on /security (closes siblings, toggles target).
    window.secToggle = function (id) {
        const el = document.getElementById(id);
        if (!el) return;
        document.querySelectorAll('.sec-detail').forEach(function (d) {
            if (d.id !== id) d.style.display = 'none';
        });
        const hidden = getComputedStyle(el).display === 'none';
        el.style.display = hidden ? 'block' : 'none';
        if (el.style.display === 'block') {
            el.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        }
    };

    // Drawer open/close — CSS-class toggle only. Content loads via HTMX.
    function openSecDrawer() {
        document.getElementById('sec-drawer').classList.add('sec-drawer--open');
        document.getElementById('sec-drawer-overlay').classList.add('sec-drawer-overlay--open');
    }
    window.closeSecDrawer = function () {
        document.getElementById('sec-drawer').classList.remove('sec-drawer--open');
        document.getElementById('sec-drawer-overlay').classList.remove('sec-drawer-overlay--open');
    };

    // "Scan Details" click → HTMX-load the drawer fragment, then open.
    document.addEventListener('click', function (e) {
        const btn = e.target.closest('.sec-detail-btn');
        if (!btn) return;
        const envoyID = btn.dataset.id;
        if (!envoyID) return;
        if (window.htmx) {
            window.htmx.ajax('GET', '/ui/fragments/security-envoy/' + envoyID, '#sec-drawer-content');
        }
        openSecDrawer();
    });

    // Overlay click closes the drawer.
    document.addEventListener('click', function (e) {
        if (e.target && e.target.id === 'sec-drawer-overlay') window.closeSecDrawer();
    });

    // In-drawer severity filter. Pane is server-rendered; this toggles
    // which cards are visible based on [data-sev] — no data fetching.
    document.addEventListener('click', function (e) {
        const btn = e.target.closest('.sec-filter__btn');
        if (!btn) return;
        const bar = btn.parentElement;
        bar.querySelectorAll('.sec-filter__btn').forEach(function (b) {
            b.classList.remove('sec-filter__btn--active');
        });
        btn.classList.add('sec-filter__btn--active');
        const sev = btn.getAttribute('data-sev');
        const pane = bar.closest('.sec-tab-pane') || bar.parentElement;
        pane.querySelectorAll('.sec-card[data-sev]').forEach(function (card) {
            card.style.display = (sev === 'all' || card.getAttribute('data-sev') === sev) ? '' : 'none';
        });
    });
})();
