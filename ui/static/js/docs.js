// Docs search. The page index ({t:title, p:path, x:text} per page) is emitted
// server-side as a <script type="application/json" id="docs-search-index">
// data block (not executable — allowed under script-src 'self') and parsed
// here, instead of being interpolated into an inline <script>. Section toggle
// lives in ui-actions.js (window.toggleDocsSection).
(function () {
    var dataEl = document.getElementById("docs-search-index");
    if (!dataEl) return;
    var pages;
    try {
        pages = JSON.parse(dataEl.textContent);
    } catch (e) {
        pages = [];
    }
    // Expose for the AI drawer's docs context (preserves prior behavior).
    window._vigoDocsPages = pages;

    var input = document.getElementById("docs-search");
    var results = document.getElementById("docs-search-results");
    var tree = document.getElementById("docs-nav-tree");
    if (!input) return;

    function snippet(text, terms) {
        var lower = text.toLowerCase();
        var best = -1;
        for (var i = 0; i < terms.length; i++) {
            var pos = lower.indexOf(terms[i]);
            if (pos !== -1 && (best === -1 || pos < best)) best = pos;
        }
        if (best === -1) return "";
        var start = Math.max(0, best - 40);
        var end = Math.min(text.length, best + 80);
        var s = (start > 0 ? "..." : "") + text.substring(start, end) + (end < text.length ? "..." : "");
        for (var k = 0; k < terms.length; k++) {
            var re = new RegExp("(" + terms[k].replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + ")", "gi");
            s = s.replace(re, "<mark>$1</mark>");
        }
        return s;
    }

    input.addEventListener("input", function () {
        var q = this.value.trim().toLowerCase();
        if (!q) {
            // Visibility is governed by the `u-d-none` class (display:none),
            // not inline style — toggle the class so it actually shows/hides.
            results.classList.add("u-d-none");
            tree.classList.remove("u-d-none");
            return;
        }
        tree.classList.add("u-d-none");
        results.classList.remove("u-d-none");
        var html = "";
        var terms = q.split(/\s+/);
        var hits = [];
        for (var i = 0; i < pages.length; i++) {
            var title = pages[i].t.toLowerCase();
            var path = pages[i].p.toLowerCase();
            var text = pages[i].x.toLowerCase();
            var match = true;
            var inTitle = true;
            for (var j = 0; j < terms.length; j++) {
                if (title.indexOf(terms[j]) !== -1 || path.indexOf(terms[j]) !== -1) continue;
                if (text.indexOf(terms[j]) !== -1) { inTitle = false; continue; }
                match = false;
                break;
            }
            if (match) hits.push({ page: pages[i], titleMatch: inTitle });
        }
        hits.sort(function (a, b) { return (b.titleMatch ? 1 : 0) - (a.titleMatch ? 1 : 0); });

        for (var h = 0; h < hits.length; h++) {
            var p = hits[h].page;
            var section = p.p.split("/")[0].replace(/-/g, " ");
            var snip = hits[h].titleMatch ? "" : snippet(p.x, terms);
            html += '<a href="/docs/' + p.p + '" class="docs-nav__search-hit">' +
                '<span class="docs-nav__search-hit-title">' + p.t + "</span>" +
                '<span class="docs-nav__search-hit-section">' + section + "</span>" +
                (snip ? '<span class="docs-nav__search-hit-snippet">' + snip + "</span>" : "") +
                "</a>";
        }
        results.innerHTML = html || '<div class="docs-nav__no-results">No pages found</div>';
    });

    input.addEventListener("keydown", function (e) {
        if (e.key === "Escape") {
            this.value = "";
            this.dispatchEvent(new Event("input"));
            this.blur();
        }
    });
})();
