// Fleet topology — D3 force-directed bubble chart.
// Each envoy is a circle: size = module count, color = convergence status.
// Reads data from <script id="fleet-topology-data" type="application/json">.

(function() {
    var svg, simulation;
    var width = 0, height = 0;

    function readData() {
        var el = document.getElementById('fleet-topology-data');
        if (!el) return null;
        try { return JSON.parse(el.textContent); } catch(e) { return null; }
    }

    // Node colors from the active theme's CSS vars so the chart harmonizes
    // with chrome in both Mocha and Latte. Falls back to a neutral hex if
    // a var isn't defined yet (e.g., during early paint).
    function themeColors() {
        var cs = getComputedStyle(document.documentElement);
        function v(name, fallback) {
            var s = cs.getPropertyValue(name);
            s = s && s.trim();
            return s || fallback;
        }
        return {
            fallback: v('--color-muted', '#6c7086'),
            stroke:   v('--color-bg', '#1e1e2e'),
            label:    v('--color-text', '#cdd6f4')
        };
    }

    function initTopology() {
        var container = document.getElementById('fleet-topology-container');
        if (!container) return;
        var data = readData();
        if (!data || data.length === 0) return;

        width = container.clientWidth || 600;
        height = container.clientHeight || 400;

        container.innerHTML = '';

        svg = d3.select(container).append('svg')
            .attr('width', width)
            .attr('height', height);

        var nodeGroup = svg.append('g');
        var labelGroup = svg.append('g');

        var tc = themeColors();

        // Create nodes.
        var nodes = nodeGroup.selectAll('circle')
            .data(data, function(d) { return d.id; })
            .join('circle')
            .attr('r', function(d) { return d.radius || 10; })
            .attr('fill', function(d) { return d.color || tc.fallback; })
            .attr('stroke', tc.stroke)
            .attr('stroke-width', 1.5)
            .attr('opacity', 0.85)
            .call(d3.drag()
                .on('start', dragStarted)
                .on('drag', dragged)
                .on('end', dragEnded));

        nodes.append('title')
            .text(function(d) {
                return d.hostname + ' (' + d.status + ')' +
                    '\nModules: ' + d.moduleCount +
                    '\nFailure: ' + (d.failurePct || 0).toFixed(1) + '%';
            });

        // Labels.
        var labels = labelGroup.selectAll('text')
            .data(data, function(d) { return d.id; })
            .join('text')
            .attr('font-size', 10)
            .attr('font-family', 'monospace')
            .attr('fill', tc.label)
            .attr('text-anchor', 'middle')
            .attr('dy', function(d) { return -(d.radius || 10) - 4; })
            .text(function(d) { return d.hostname; });

        // Force simulation — no links, just clustering by collision.
        simulation = d3.forceSimulation(data)
            .force('charge', d3.forceManyBody().strength(-80))
            .force('center', d3.forceCenter(width / 2, height / 2))
            .force('collision', d3.forceCollide().radius(function(d) { return (d.radius || 10) + 4; }))
            .on('tick', function() {
                nodes
                    .attr('cx', function(d) { return d.x; })
                    .attr('cy', function(d) { return d.y; });
                labels
                    .attr('x', function(d) { return d.x; })
                    .attr('y', function(d) { return d.y; });
            });
    }

    function dragStarted(event, d) {
        if (!event.active) simulation.alphaTarget(0.3).restart();
        d.fx = d.x;
        d.fy = d.y;
    }

    function dragged(event, d) {
        d.fx = event.x;
        d.fy = event.y;
    }

    function dragEnded(event, d) {
        if (!event.active) simulation.alphaTarget(0);
        d.fx = null;
        d.fy = null;
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initTopology);
    } else {
        setTimeout(initTopology, 0);
    }

    document.body.addEventListener('htmx:afterSettle', function(e) {
        if (e.detail.target && e.detail.target.id === 'dashboard-viz-fleet') {
            initTopology();
        }
    });

    // Re-paint chrome colors (stroke, label) when the theme flips. Per-node
    // fills come from server-provided data and are Catppuccin accents that
    // read on both backgrounds, so they stay stable across themes.
    document.addEventListener('theme-changed', function() {
        if (!svg) return;
        var tc = themeColors();
        svg.selectAll('circle').attr('stroke', tc.stroke);
        svg.selectAll('text').attr('fill', tc.label);
    });
})();
