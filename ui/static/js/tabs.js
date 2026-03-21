// setActiveTab toggles the btn--primary class on tab button groups.
// Used by envoy-detail and other tabbed interfaces.
function setActiveTab(btn) {
    btn.closest('.filter-bar').querySelectorAll('.btn').forEach(function(b) {
        b.classList.remove('btn--primary');
    });
    btn.classList.add('btn--primary');
}
