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

// Restore sidebar state on load.
(function() {
    try {
        if (localStorage.getItem('vigo_sidebar') === 'collapsed') {
            document.body.classList.add('sidebar--collapsed');
        }
    } catch(e) {}
})();
