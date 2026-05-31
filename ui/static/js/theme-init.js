// Apply theme class on <html> before first paint so there's no flash of the
// wrong theme. Order of precedence: localStorage override → branding default
// (read from <html data-branding-theme>) → OS preference. Class goes on <html>
// so it survives HTMX body swaps. Values other than "light"/"dark" are ignored
// to keep the set closed. Loaded synchronously in <head> (render-blocking) so
// it runs before the body paints — moved out of an inline <script> so the CSP
// can drop script-src 'unsafe-inline'.
(function () {
    var d = document.documentElement;
    var stored = null;
    try { stored = localStorage.getItem("vigo_theme"); } catch (e) {}
    var branding = d.getAttribute("data-branding-theme") || "";
    var resolved;
    if (stored === "light" || stored === "dark") {
        resolved = stored;
    } else if (branding === "light" || branding === "dark") {
        resolved = branding;
    } else {
        resolved = (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches) ? "dark" : "light";
    }
    d.classList.add(resolved === "dark" ? "theme-dark" : "theme-light");
})();
