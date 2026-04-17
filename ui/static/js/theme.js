// Theme toggle: flips between Catppuccin Mocha (dark) and Latte (light).
// The initial theme class is applied by the inline <head> script in base.html
// before first paint. This file wires up the toggle button, cross-tab sync,
// and emits a "theme-changed" CustomEvent for D3 visualizations that need to
// re-source CSS var colors when the palette flips.
(function () {
    var STORAGE_KEY = "vigo_theme";

    function currentTheme() {
        return document.documentElement.classList.contains("theme-dark") ? "dark" : "light";
    }

    function applyTheme(theme) {
        var d = document.documentElement;
        if (theme === "dark") {
            d.classList.add("theme-dark");
            d.classList.remove("theme-light");
        } else {
            d.classList.add("theme-light");
            d.classList.remove("theme-dark");
        }
        document.dispatchEvent(new CustomEvent("theme-changed", { detail: { theme: theme } }));
    }

    function toggle() {
        var next = currentTheme() === "dark" ? "light" : "dark";
        try { localStorage.setItem(STORAGE_KEY, next); } catch (e) {}
        applyTheme(next);
    }

    function wireButton() {
        var btn = document.getElementById("theme-toggle");
        if (!btn || btn.dataset.wired === "1") return;
        btn.dataset.wired = "1";
        btn.addEventListener("click", toggle);
    }

    // Cross-tab sync: when another tab writes to localStorage, follow along.
    window.addEventListener("storage", function (e) {
        if (e.key !== STORAGE_KEY) return;
        if (e.newValue === "light" || e.newValue === "dark") {
            applyTheme(e.newValue);
        }
    });

    // HTMX boosted navigation may re-run scripts but wipes body contents; the
    // theme class lives on <html> so it survives, but the toggle button is
    // inside the body and needs re-wiring after each swap.
    document.addEventListener("htmx:afterSettle", wireButton);

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", wireButton);
    } else {
        wireButton();
    }
})();
