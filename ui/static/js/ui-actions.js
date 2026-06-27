// Delegated UI action dispatcher. A small set of document-level listeners
// replace the inline on* handlers that used to live in templates and
// Go-emitted HTML, so the Content-Security-Policy can drop
// script-src 'unsafe-inline' (the severe half — arbitrary JS execution).
//
// Why delegation: one listener on `document` survives every HTMX swap and
// hx-boost navigation without re-attachment. Markup opts in with
// `data-action="..."`; parameters ride other `data-*` attributes (the same
// pattern the AI drawer already uses with `data-question`).
//
// Domain behaviors stay in their owning modules (toggleAIChat → ai-drawer.js,
// setActiveTab/toggleSidebar → tabs.js, secToggle → security-drawer.js,
// toggleEnvoyFilterDrawer → filter-drawer.js). This file only routes events
// to them and hosts the few small behaviors that were previously inline-only.
(function () {
    "use strict";

    // Behaviors that previously lived in per-page inline <script> blocks.

    // docs.html section toggle. The markup hides collapsed pages with the
    // `u-d-none` utility class (since the CSP de-inlining), so toggle that
    // class — an inline style.display can't override the class's `display:none`.
    window.toggleDocsSection = function (btn) {
        var section = btn.closest(".docs-nav__section");
        if (!section) return;
        var pages = section.querySelector(".docs-nav__pages");
        if (!pages) return;
        var nowHidden = pages.classList.toggle("u-d-none");
        btn.classList.toggle("docs-nav__section-toggle--active", !nowHidden);
    };

    // compliance-evidence.html status filter. Toggles each control row and its
    // sibling drill-down row, and the active filter tab.
    window.filterControls = function (status, btn) {
        document.querySelectorAll("#controls-table tbody tr[data-status]").forEach(function (row) {
            var drill = row.nextElementSibling;
            var show = (status === "all" || row.dataset.status === status);
            row.style.display = show ? "" : "none";
            if (drill) drill.style.display = show ? "" : "none";
        });
        document.querySelectorAll(".filter-tab").forEach(function (t) {
            t.classList.toggle("filter-tab--active", t === btn);
        });
    };

    // inventory trait-index modal: filter the trait list as the operator types.
    function traitIndexFilter(input) {
        var q = (input.value || "").toLowerCase();
        var rows = document.querySelectorAll("#trait-index-body [data-trait-key]");
        var hdrs = document.querySelectorAll("#trait-index-body [data-ns-header]");
        rows.forEach(function (r) {
            r.style.display = (!q || r.getAttribute("data-trait-key").toLowerCase().indexOf(q) !== -1) ? "" : "none";
        });
        hdrs.forEach(function (h) {
            var ns = h.getAttribute("data-ns-header");
            var any = false;
            rows.forEach(function (r) {
                if (r.getAttribute("data-ns") === ns && r.style.display !== "none") any = true;
            });
            h.style.display = (any || !q) ? "" : "none";
        });
    }

    function clearEl(id) {
        var el = document.getElementById(id);
        if (el) el.innerHTML = "";
    }

    // --- click ---------------------------------------------------------------
    document.addEventListener("click", function (e) {
        // Backdrop close: only when the overlay element itself is the target
        // (clicks on its children don't close it).
        if (e.target.matches && e.target.matches("[data-overlay-close]")) {
            clearEl(e.target.dataset.overlayClose);
            return;
        }
        var el = e.target.closest("[data-action]");
        if (!el) return;
        switch (el.dataset.action) {
            case "toggle-ai-chat": if (typeof toggleAIChat === "function") toggleAIChat(); break;
            case "ai-suggest": if (typeof aiDrawerSuggest === "function") aiDrawerSuggest(el); break;
            case "toggle-sidebar": if (typeof toggleSidebar === "function") toggleSidebar(); break;
            case "toggle-filter-drawer": if (typeof toggleEnvoyFilterDrawer === "function") toggleEnvoyFilterDrawer(); break;
            case "sec-toggle": if (typeof secToggle === "function") secToggle(el.dataset.panel); break;
            case "close-sec-drawer": if (typeof closeSecDrawer === "function") closeSecDrawer(); break;
            case "set-active-tab": if (typeof setActiveTab === "function") setActiveTab(el); break;
            case "toggle-docs-section": window.toggleDocsSection(el); break;
            case "filter-controls": window.filterControls(el.dataset.filter, el); break;
            case "show-waiver": el.dataset.showWaiver = "1"; break;
            case "nav": if (el.dataset.href) location.href = el.dataset.href; break;
            case "close-trait-modal": clearEl("trait-index-modal"); break;
            case "close-trait-explore": clearEl("trait-explore-panel"); clearEl("trait-search-results"); break;
            case "toggle-docs-row": {
                var row = document.getElementById(el.dataset.target);
                if (row && row.parentElement) {
                    var p = row.parentElement;
                    p.style.display = p.style.display === "table-row" ? "none" : "table-row";
                }
                break;
            }
            case "toggle-run-results": {
                var tr = el.closest("tr");
                if (!tr || !tr.nextElementSibling) break;
                var t = tr.nextElementSibling.querySelector(".run-results");
                if (!t) break;
                if (t.innerHTML.trim()) {
                    t.innerHTML = "";
                } else if (window.htmx && el.dataset.runId) {
                    htmx.ajax("GET", "/ui/fragments/run-results/" + el.dataset.runId, { target: t, swap: "innerHTML" });
                }
                break;
            }
        }
    });

    // --- input (data-input) --------------------------------------------------
    document.addEventListener("input", function (e) {
        var el = e.target.closest("[data-input]");
        if (!el) return;
        switch (el.dataset.input) {
            case "filter-pills": if (typeof filterEnvoyFilterPills === "function") filterEnvoyFilterPills(el); break;
            case "trait-filter": traitIndexFilter(el); break;
            case "ai-autosize":
                el.style.height = "auto";
                el.style.height = Math.min(el.scrollHeight, 120) + "px";
                break;
        }
    });

    // --- change (data-change) ------------------------------------------------
    document.addEventListener("change", function (e) {
        var el = e.target.closest("[data-change]");
        if (!el) return;
        if (el.dataset.change === "submit-form" && el.form) el.form.submit();
        // Locale picker: a display preference, like the theme toggle — set a
        // server-readable cookie client-side and reload so the next render
        // picks the chosen locale. No POST (the UI is observability-only).
        else if (el.dataset.change === "set-locale") {
            document.cookie = "vigo_lang=" + encodeURIComponent(el.value) +
                "; path=/; max-age=31536000; samesite=lax";
            window.location.reload();
        }
        // Spanner scope selector (ADR-037), header dropdown form (≥5 bolts).
        else if (el.dataset.change === "set-scope") {
            applyScope(el.value);
        }
    });

    // Spanner scope selector (ADR-037), header tab form (≤4 bolts). Tabs are
    // buttons (not boosted links) so the cookie write + full reload mirror the
    // locale picker exactly — the selection then applies to every page.
    document.addEventListener("click", function (e) {
        var el = e.target.closest("[data-set-scope]");
        if (!el) return;
        e.preventDefault();
        applyScope(el.dataset.setScope);
    });

    // applyScope persists the chosen scope in a server-readable cookie and
    // reloads onto the current page WITHOUT ?scope= so the cookie drives the
    // render (an explicit ?scope= would otherwise win over the new cookie).
    function applyScope(v) {
        document.cookie = "vigo_scope=" + encodeURIComponent(v) +
            "; path=/; max-age=31536000; samesite=lax";
        var u = new URL(window.location.href);
        u.searchParams.delete("scope");
        var target = u.pathname + u.search + u.hash;
        if (target === window.location.pathname + window.location.search + window.location.hash) {
            window.location.reload();
        } else {
            window.location.href = target;
        }
    }

    // Deep-link sync: a ?scope= URL renders that scope (resolveScope reads the
    // query first); persist it to the cookie so the choice sticks on the next
    // navigation. Runs once per full load — which is exactly when a ?scope=
    // deep link is opened.
    (function syncScopeFromQuery() {
        var s = new URLSearchParams(window.location.search).get("scope");
        if (s) {
            document.cookie = "vigo_scope=" + encodeURIComponent(s) +
                "; path=/; max-age=31536000; samesite=lax";
        }
    })();

    // --- submit (data-submit) ------------------------------------------------
    document.addEventListener("submit", function (e) {
        var el = e.target.closest("[data-submit]");
        if (!el) return;
        if (el.dataset.submit === "ai-submit" && typeof aiDrawerSubmit === "function") {
            aiDrawerSubmit(e); // the handler calls preventDefault itself
        }
    });

    // --- keydown (data-keydown) ----------------------------------------------
    document.addEventListener("keydown", function (e) {
        var el = e.target.closest("[data-keydown]");
        if (!el) return;
        if (el.dataset.keydown === "ai-enter-submit" && e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            if (typeof aiDrawerSubmit === "function") aiDrawerSubmit(e);
        }
    });

    // After the trait-index modal fragment settles in, focus its search box
    // (replaces the inline <script>…focus()</script> the fragment used to
    // emit). And once a trait's explore panel has loaded, clear the modal
    // (replaces inventory.html's hx-on::after-settle).
    document.addEventListener("htmx:afterSettle", function (e) {
        var search = document.getElementById("trait-index-search");
        if (search && e.target && e.target.contains && e.target.contains(search)) {
            search.focus();
        }
        if (e.target && e.target.id === "trait-explore-panel" && e.target.innerHTML.trim()) {
            clearEl("trait-index-modal");
        }
    });
})();
