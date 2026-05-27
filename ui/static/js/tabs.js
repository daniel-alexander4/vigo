// setActiveTab toggles the btn--primary class on tab button groups.
// Used by envoy-detail and other tabbed interfaces.
function setActiveTab(btn) {
    btn.closest('.filter-bar').querySelectorAll('.btn').forEach(function(b) {
        b.classList.remove('btn--primary');
    });
    btn.classList.add('btn--primary');
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

// activateTabFromHash clicks the tab button matching window.location.hash
// (e.g. #footprint → #tab-footprint). Lets cross-page drilldowns like
// /envoys row pills and /agent-health row links land on the right tab
// instead of the default. No-op when the hash doesn't match a tab button.
function activateTabFromHash() {
    var hash = window.location.hash;
    if (!hash || hash.length < 2) return;
    var btn = document.getElementById('tab-' + hash.slice(1));
    if (btn) btn.click();
}

document.addEventListener('DOMContentLoaded', activateTabFromHash);
window.addEventListener('hashchange', activateTabFromHash);
