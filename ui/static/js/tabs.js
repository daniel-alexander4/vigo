// setActiveTab toggles the btn--primary class on tab button groups.
// Used by envoy-detail and other tabbed interfaces. Also mirrors the
// active tab into window.location.hash so the afterSettle activator
// doesn't snap back to a stale hash and drilldowns stay bookmarkable.
// Uses replaceState to avoid firing hashchange (which would re-enter
// activateTabFromHash unnecessarily).
function setActiveTab(btn) {
    btn.closest('.filter-bar').querySelectorAll('.btn').forEach(function(b) {
        b.classList.remove('btn--primary');
    });
    btn.classList.add('btn--primary');
    if (btn.id && btn.id.indexOf('tab-') === 0) {
        var newHash = '#' + btn.id.slice(4);
        if (window.location.hash !== newHash) {
            try { history.replaceState(history.state, '', newHash); } catch (e) {}
        }
    }
}

// toggleSidebar collapses/expands the main navigation sidebar.
// State persisted in localStorage so it survives page navigation.
function toggleSidebar() {
    var body = document.body;
    body.classList.toggle('sidebar--collapsed');
    try {
        localStorage.setItem('vigo_sidebar', body.classList.contains('sidebar--collapsed') ? 'collapsed' : 'expanded');
    } catch(e) {}
}

// Restore sidebar state from localStorage.
function restoreSidebarState() {
    try {
        if (localStorage.getItem('vigo_sidebar') === 'collapsed') {
            document.body.classList.add('sidebar--collapsed');
        }
    } catch(e) {}
}

// Restore on initial page load.
document.addEventListener('DOMContentLoaded', restoreSidebarState);

// Restore after hx-boost navigation (HTMX replaces body classes from
// the server response, stripping the client-side sidebar--collapsed class).
document.addEventListener('htmx:afterSettle', restoreSidebarState);

// Preserve sidebar scroll position across hx-boost navigation. With
// hx-boost on <body>, every menu click does an outerHTML swap of body —
// the sidebar element gets re-created and its scrollTop zeroes. Stash
// scroll on every swap-out and restore on every settle; polling
// fragments are a no-op (save current scroll → restore the same value).
// requestAnimationFrame defers the restore until after the new sidebar
// has had its layout computed — setting scrollTop on a not-yet-laid-out
// element silently no-ops in some browsers.
// sessionStorage (not localStorage) scopes scroll per-tab and clears
// on tab close — viewport state, not user preference.
function saveSidebarScroll() {
    var sb = document.querySelector('.sidebar');
    if (sb) {
        try { sessionStorage.setItem('vigo_sidebar_scroll', sb.scrollTop); } catch (err) {}
    }
}
function restoreSidebarScroll() {
    try {
        var pos = sessionStorage.getItem('vigo_sidebar_scroll');
        if (pos === null) return;
        var top = parseInt(pos, 10) || 0;
        requestAnimationFrame(function() {
            var sb = document.querySelector('.sidebar');
            if (sb) sb.scrollTop = top;
        });
    } catch (err) {}
}
document.addEventListener('htmx:beforeSwap', saveSidebarScroll);
document.addEventListener('htmx:afterSettle', restoreSidebarScroll);

// activateTabFromHash clicks the tab button matching window.location.hash
// (e.g. #footprint → #tab-footprint) on init. When the hash is absent or
// doesn't resolve to a tab, clicks the first tab in the bar so the page
// always renders exactly one tab fragment. The single click avoids the
// race where hx-trigger="load" on the content container would otherwise
// load the default fragment underneath the hash-resolved one.
// Idempotent: if the resolved tab is already active (btn--primary), do
// nothing. Without this guard, htmx:afterSettle from a user's later tab
// click would re-fire and snap them back to the hash-resolved tab.
function activateTabFromHash() {
    var firstTab = document.querySelector('.filter-bar [id^="tab-"]');
    if (!firstTab) return;
    var hash = window.location.hash;
    var btn = null;
    if (hash && hash.length > 1) {
        btn = document.getElementById('tab-' + hash.slice(1));
    }
    if (!btn) btn = firstTab.closest('.filter-bar').querySelector('[id^="tab-"]');
    if (!btn || btn.classList.contains('btn--primary')) return;
    btn.click();
}

// DOMContentLoaded covers the initial page load; htmx:afterSettle covers
// boosted navigations (hx-boost="true" on <body>) — those don't fire
// DOMContentLoaded. hashchange catches in-page hash edits.
document.addEventListener('DOMContentLoaded', activateTabFromHash);
document.addEventListener('htmx:afterSettle', activateTabFromHash);
window.addEventListener('hashchange', activateTabFromHash);
