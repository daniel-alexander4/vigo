// Swarm chunk-flow Sankey diagram.
// Reads data from <script id="topology-data" type="application/json">.
// Supports HTMX auto-refresh via htmx:afterSettle.
//
// Data shape (unchanged from the old force-graph renderer):
//   { nodes: [{id, color, ...}], edges: [{source, target, chunks}] }
//
// Layout: nodes are assigned a column by longest path from any pure source.
// Typical case (one seeder → many receivers) lays out as two columns; peer
// exchange where a receiver also serves chunks downstream becomes three.
// Within each column, nodes are sorted and stacked with vertical padding.
// Node height is proportional to max(incoming, outgoing) flow so the visual
// weight matches the throughput. Ribbons are cubic beziers with stroke-width
// proportional to chunks transferred, colored by the source envoy.

(function() {
    var initialized = false;

    function readData() {
        var el = document.getElementById('topology-data');
        if (!el) return null;
        try { return JSON.parse(el.textContent); } catch(e) { return null; }
    }

    // Compute each node's column via a longest-path-from-source relaxation.
    // Runs in O(V*E) worst case which is trivially fine for any fleet size
    // that fits in a swarm detail page (tens to low hundreds of envoys).
    function assignDepths(nodes, edges) {
        var depth = {};
        nodes.forEach(function(n) { depth[n.id] = 0; });
        var changed = true;
        var guard = 0;
        while (changed && guard < nodes.length + 2) {
            changed = false;
            guard += 1;
            edges.forEach(function(e) {
                var src = typeof e.source === 'object' ? e.source.id : e.source;
                var tgt = typeof e.target === 'object' ? e.target.id : e.target;
                if (depth[tgt] <= depth[src]) {
                    depth[tgt] = depth[src] + 1;
                    changed = true;
                }
            });
        }
        return depth;
    }

    function initSankey() {
        var container = document.getElementById('topology-container');
        if (!container) return;
        var data = readData();
        if (!data || !data.nodes || data.nodes.length === 0) return;

        var width = container.clientWidth || 600;
        var height = container.clientHeight || 400;

        // Clear previous content.
        container.innerHTML = '';

        if (!data.edges || data.edges.length === 0) {
            // No chunk transfers yet — render a helpful empty state instead
            // of an empty SVG so the card doesn't look broken during the
            // first few seconds of a distribution.
            var empty = document.createElement('div');
            empty.style.cssText = 'display:flex;align-items:center;justify-content:center;height:100%;color:var(--color-text-muted, #718096);font-size:13px;';
            empty.textContent = 'no chunk transfers recorded yet';
            container.appendChild(empty);
            initialized = true;
            return;
        }

        var svg = d3.select(container).append('svg')
            .attr('width', width)
            .attr('height', height);

        // Build node lookup.
        var nodeById = {};
        data.nodes.forEach(function(n) {
            nodeById[n.id] = n;
            n.inFlow = 0;
            n.outFlow = 0;
            n.inSeen = 0;   // cumulative ribbon offset on incoming side
            n.outSeen = 0;  // cumulative ribbon offset on outgoing side
        });

        // Aggregate flow per node. Drop edges that point at unknown nodes
        // (defensive — the Go side always emits both endpoints, but this
        // keeps a malformed payload from crashing the renderer).
        var validEdges = [];
        data.edges.forEach(function(e) {
            if (!nodeById[e.source] || !nodeById[e.target]) return;
            nodeById[e.source].outFlow += e.chunks;
            nodeById[e.target].inFlow += e.chunks;
            validEdges.push(e);
        });

        if (validEdges.length === 0) {
            initialized = true;
            return;
        }

        // Column assignment via longest-path depth.
        var depth = assignDepths(data.nodes, validEdges);
        var maxDepth = 0;
        data.nodes.forEach(function(n) {
            n.col = depth[n.id];
            if (n.col > maxDepth) maxDepth = n.col;
        });

        var columns = [];
        for (var c = 0; c <= maxDepth; c++) columns.push([]);
        data.nodes.forEach(function(n) {
            // Drop orphan nodes (no flow in or out) from the layout entirely —
            // they'd otherwise eat vertical space in column 0 without showing
            // any ribbons. The operator can see non-participating envoys on
            // the status table above.
            if (n.inFlow === 0 && n.outFlow === 0) {
                n.skip = true;
                return;
            }
            columns[n.col].push(n);
        });

        // Drop empty columns (can happen if every node in a column is an
        // orphan we just skipped).
        columns = columns.filter(function(col) { return col.length > 0; });
        var numCols = columns.length;
        if (numCols === 0) {
            initialized = true;
            return;
        }

        // Layout geometry. Label gutter on both sides so hostname text
        // doesn't clip against the container edge.
        var labelGutter = 80;
        var topMargin = 24;
        var botMargin = 24;
        var nodeWidth = 14;
        var nodePadding = 20;
        var chartLeft = labelGutter;
        var chartRight = width - labelGutter;
        var chartTop = topMargin;
        var chartBottom = height - botMargin;
        var chartHeight = chartBottom - chartTop;
        var chartWidth = chartRight - chartLeft;

        // Find max flow per column so each column's node heights scale
        // against its own availability — avoids one giant column shoving
        // every other column into a sliver.
        var maxColFlow = columns.map(function(col) {
            var total = 0;
            col.forEach(function(n) { total += Math.max(n.inFlow, n.outFlow, 1); });
            return total;
        });

        // Compute node positions. Columns are evenly spaced horizontally;
        // within a column, nodes stack vertically with height proportional
        // to their flow share. Alphabetic sort keeps the stack order stable
        // across refreshes so ribbons don't reshuffle every 5 seconds.
        columns.forEach(function(col, colIdx) {
            col.sort(function(a, b) { return a.id.localeCompare(b.id); });

            var total = maxColFlow[colIdx] || 1;
            var availHeight = chartHeight - (col.length - 1) * nodePadding;
            if (availHeight < col.length * 8) {
                // Not enough room for 8px per node — give each an equal slice.
                availHeight = col.length * 8;
            }

            var x;
            if (numCols === 1) {
                x = (chartLeft + chartRight) / 2 - nodeWidth / 2;
            } else {
                x = chartLeft + (colIdx / (numCols - 1)) * (chartWidth - nodeWidth);
            }

            var y = chartTop;
            col.forEach(function(n) {
                var flow = Math.max(n.inFlow, n.outFlow, 1);
                var h = Math.max(12, (flow / total) * availHeight);
                n.x = x;
                n.y = y;
                n.h = h;
                n.w = nodeWidth;
                y += h + nodePadding;
            });
        });

        // Sort edges so ribbons attaching to each node stack in a stable
        // order. Within a source, ribbons are ordered by the target's y
        // position (top-to-bottom); within a target, by the source's y.
        // This mirrors d3-sankey's vertical stacking.
        var outEdges = {};
        var inEdges = {};
        validEdges.forEach(function(e) {
            (outEdges[e.source] = outEdges[e.source] || []).push(e);
            (inEdges[e.target] = inEdges[e.target] || []).push(e);
        });
        Object.keys(outEdges).forEach(function(id) {
            outEdges[id].sort(function(a, b) {
                return nodeById[a.target].y - nodeById[b.target].y;
            });
        });
        Object.keys(inEdges).forEach(function(id) {
            inEdges[id].sort(function(a, b) {
                return nodeById[a.source].y - nodeById[b.source].y;
            });
        });

        // Assign each edge its width and its y-offset within both endpoints.
        // The offset drives where the ribbon attaches so multiple ribbons
        // fanning out from one node stack cleanly instead of piling on the
        // midpoint.
        function ribbonWidthFor(node, chunks) {
            var nodeFlow = Math.max(node.inFlow, node.outFlow, 1);
            return (chunks / nodeFlow) * node.h;
        }

        var linksLayer = svg.append('g').attr('class', 'sankey-links');
        var ribbonOpacity = 0.45;

        Object.keys(outEdges).forEach(function(id) {
            var src = nodeById[id];
            var offset = 0;
            outEdges[id].forEach(function(e) {
                var w = ribbonWidthFor(src, e.chunks);
                e.srcY = src.y + offset + w / 2;
                e.srcW = w;
                offset += w;
            });
        });
        Object.keys(inEdges).forEach(function(id) {
            var tgt = nodeById[id];
            var offset = 0;
            inEdges[id].forEach(function(e) {
                var w = ribbonWidthFor(tgt, e.chunks);
                e.tgtY = tgt.y + offset + w / 2;
                e.tgtW = w;
                offset += w;
            });
        });

        // Draw ribbons. Stroke-width uses the AVERAGE of src/tgt widths to
        // avoid a ribbon looking pinched at one end just because the src
        // and tgt have very different total flows.
        validEdges.forEach(function(e) {
            var src = nodeById[e.source];
            var tgt = nodeById[e.target];
            var x0 = src.x + src.w;
            var x1 = tgt.x;
            var midX = (x0 + x1) / 2;
            var d = 'M' + x0 + ',' + e.srcY +
                    ' C' + midX + ',' + e.srcY +
                    ' ' + midX + ',' + e.tgtY +
                    ' ' + x1 + ',' + e.tgtY;
            var w = Math.max(1, (e.srcW + e.tgtW) / 2);
            linksLayer.append('path')
                .attr('d', d)
                .attr('fill', 'none')
                .attr('stroke', src.color || '#9399b2')
                .attr('stroke-width', w)
                .attr('stroke-opacity', ribbonOpacity)
                .append('title')
                .text(e.source + ' → ' + e.target + ': ' + e.chunks + ' chunks');
        });

        // Draw nodes. Use rectangles (standard Sankey node) colored by the
        // envoy's hostname palette entry. The rectangle width is constant;
        // height encodes flow. Rounded corners (rx=2) soften the look.
        var nodesLayer = svg.append('g').attr('class', 'sankey-nodes');
        data.nodes.forEach(function(n) {
            if (n.skip) return;
            nodesLayer.append('rect')
                .attr('x', n.x)
                .attr('y', n.y)
                .attr('width', n.w)
                .attr('height', n.h)
                .attr('fill', n.color || '#a0aec0')
                .attr('rx', 2)
                .append('title')
                .text(n.id + ' (in: ' + n.inFlow + ', out: ' + n.outFlow + ')');
        });

        // Draw hostname labels. Labels on the leftmost column sit to the
        // left of their node; labels on the rightmost column sit to the
        // right. Middle-column labels (rare — only appears when an envoy
        // re-serves chunks downstream) sit above the node.
        var labelsLayer = svg.append('g').attr('class', 'sankey-labels');
        data.nodes.forEach(function(n) {
            if (n.skip) return;
            var isLeft = n.col === 0;
            var isRight = n.col === maxDepth && numCols > 1;
            var tx, ty, anchor;
            if (isLeft && numCols > 1) {
                tx = n.x - 6;
                ty = n.y + n.h / 2 + 4;
                anchor = 'end';
            } else if (isRight) {
                tx = n.x + n.w + 6;
                ty = n.y + n.h / 2 + 4;
                anchor = 'start';
            } else {
                tx = n.x + n.w / 2;
                ty = n.y - 6;
                anchor = 'middle';
            }
            labelsLayer.append('text')
                .attr('x', tx)
                .attr('y', ty)
                .attr('text-anchor', anchor)
                .attr('fill', n.color || 'var(--color-text, #2d3748)')
                .attr('font-size', 11)
                .attr('font-family', 'monospace')
                .attr('font-weight', '600')
                .text(n.id);
        });

        initialized = true;
    }

    // Initialize on page load.
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initSankey);
    } else {
        // DOMContentLoaded already fired (deferred script).
        setTimeout(initSankey, 0);
    }

    // Re-initialize after HTMX swap so the Sankey re-renders against fresh
    // topology data when the envoy fragment auto-refreshes.
    document.body.addEventListener('htmx:afterSettle', function(e) {
        if (e.detail.target && e.detail.target.id === 'swarm-viz-envoys') {
            initSankey();
        }
    });
})();
