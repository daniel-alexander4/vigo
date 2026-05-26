(function() {
    // The script tag rides the body swap on every boosted nav. Guard
    // against reinit so listeners registered earlier still drive state.
    if (window.__vigoFilterDrawerLoaded) return;
    window.__vigoFilterDrawerLoaded = true;

    var STORAGE_KEY = 'vigo_envoy_filter_drawer_open';

    function isOpen() {
        try { return sessionStorage.getItem(STORAGE_KEY) === '1'; }
        catch (e) { return false; }
    }
    function setOpen(value) {
        try {
            if (value) sessionStorage.setItem(STORAGE_KEY, '1');
            else sessionStorage.removeItem(STORAGE_KEY);
        } catch (e) {}
    }

    function applyOpenClass() {
        var d = document.getElementById('envoy-filter-drawer');
        if (!d) return;
        if (isOpen()) d.classList.add('envoy-filter-drawer--open');
        else d.classList.remove('envoy-filter-drawer--open');
    }

    window.toggleEnvoyFilterDrawer = function() {
        var d = document.getElementById('envoy-filter-drawer');
        if (!d) return;
        var nowOpen = !d.classList.contains('envoy-filter-drawer--open');
        setOpen(nowOpen);
        applyOpenClass();
    };

    // Per-axis in-drawer search: hides pills whose label doesn't match
    // the typed substring. Pure DOM, no server round trip. Lives on the
    // window so the template's `oninput` attribute can call it without
    // wiring a per-element listener.
    window.filterEnvoyFilterPills = function(input) {
        var axis = input.closest('.envoy-filter-drawer__axis');
        if (!axis) return;
        var needle = input.value.trim().toLowerCase();
        var pills = axis.querySelectorAll('.envoy-filter-drawer__pill');
        for (var i = 0; i < pills.length; i++) {
            var label = (pills[i].getAttribute('data-pill-label') || '').toLowerCase();
            if (needle === '' || label.indexOf(needle) !== -1) {
                pills[i].removeAttribute('hidden');
            } else {
                pills[i].setAttribute('hidden', 'hidden');
            }
        }
    };

    // Restore open state on initial paint + after every boosted nav.
    document.addEventListener('DOMContentLoaded', applyOpenClass);
    document.body.addEventListener('htmx:afterSettle', applyOpenClass);
    applyOpenClass();
})();
