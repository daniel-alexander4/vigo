// Swarm peer topology — D3 force-directed graph.
// Reads data from <script id="topology-data" type="application/json">.
// Supports HTMX auto-refresh via htmx:afterSettle.

(function() {
    var svg, simulation, nodeGroup, linkGroup, labelGroup;
    var width = 0, height = 0;
    var initialized = false;

    var statusColors = {
        complete: '#a6e3a1',   // Catppuccin Green
        downloading: '#f9e2af', // Catppuccin Yellow
        pending: '#6c7086',    // Catppuccin Overlay0
        skipped: '#f38ba8'     // Catppuccin Red
    };

    function readData() {
        var el = document.getElementById('topology-data');
        if (!el) return null;
        try { return JSON.parse(el.textContent); } catch(e) { return null; }
    }

    function initTopology() {
        var container = document.getElementById('topology-container');
        if (!container) return;
        var data = readData();
        if (!data || !data.nodes || data.nodes.length === 0) return;

        width = container.clientWidth || 600;
        height = container.clientHeight || 400;

        // Clear previous content.
        container.innerHTML = '';

        svg = d3.select(container).append('svg')
            .attr('width', width)
            .attr('height', height);

        // Arrow markers for directed edges.
        svg.append('defs').append('marker')
            .attr('id', 'arrowhead')
            .attr('viewBox', '0 -5 10 10')
            .attr('refX', 20)
            .attr('refY', 0)
            .attr('markerWidth', 6)
            .attr('markerHeight', 6)
            .attr('orient', 'auto')
            .append('path')
            .attr('d', 'M0,-5L10,0L0,5')
            .attr('fill', '#9399b2');

        linkGroup = svg.append('g');
        nodeGroup = svg.append('g');
        labelGroup = svg.append('g');

        // Find max edge weight for scaling.
        var maxChunks = d3.max(data.edges, function(d) { return d.chunks; }) || 1;

        // Create links.
        var links = linkGroup.selectAll('line')
            .data(data.edges)
            .join('line')
            .attr('class', 'link')
            .attr('stroke-width', function(d) { return Math.max(1, (d.chunks / maxChunks) * 6); })
            .attr('stroke', '#9399b2')
            .attr('stroke-opacity', 0.6)
            .attr('marker-end', 'url(#arrowhead)');

        // Create nodes.
        var nodes = nodeGroup.selectAll('circle')
            .data(data.nodes, function(d) { return d.id; })
            .join('circle')
            .attr('class', 'node')
            .attr('r', function(d) { return d.isSource ? 14 : 10; })
            .attr('fill', function(d) { return statusColors[d.status] || '#a0aec0'; })
            .attr('stroke', '#fff')
            .attr('stroke-width', 2)
            .call(d3.drag()
                .on('start', dragStarted)
                .on('drag', dragged)
                .on('end', dragEnded));

        // Tooltips.
        nodes.append('title')
            .text(function(d) {
                return d.id + ' (' + d.status + ')' + (d.chunks ? ' — ' + d.chunks + ' chunks' : '');
            });

        // Labels.
        var labels = labelGroup.selectAll('text')
            .data(data.nodes, function(d) { return d.id; })
            .join('text')
            .attr('font-size', 11)
            .attr('font-family', 'monospace')
            .attr('fill', 'var(--color-text, #2d3748)')
            .attr('text-anchor', 'middle')
            .attr('dy', -16)
            .text(function(d) { return d.id; });

        // Edge labels (chunk counts).
        linkGroup.selectAll('text')
            .data(data.edges)
            .join('text')
            .attr('font-size', 9)
            .attr('fill', 'var(--color-text-muted, #718096)')
            .attr('text-anchor', 'middle')
            .text(function(d) { return d.chunks; });

        // Force simulation. A weak positional force pulls every node back toward
        // the center so nodes with no edges (e.g. an in-progress envoy that
        // hasn't yet reported any chunk sources) can't escape the viewport under
        // charge repulsion.
        var padding = 30;
        simulation = d3.forceSimulation(data.nodes)
            .force('link', d3.forceLink(data.edges).id(function(d) { return d.id; }).distance(120))
            .force('charge', d3.forceManyBody().strength(-300))
            .force('center', d3.forceCenter(width / 2, height / 2))
            .force('x', d3.forceX(width / 2).strength(0.05))
            .force('y', d3.forceY(height / 2).strength(0.05))
            .force('collision', d3.forceCollide().radius(30))
            .on('tick', function() {
                // Clamp every node into the viewport so orphans stay visible.
                data.nodes.forEach(function(d) {
                    d.x = Math.max(padding, Math.min(width - padding, d.x));
                    d.y = Math.max(padding, Math.min(height - padding, d.y));
                });

                links
                    .attr('x1', function(d) { return d.source.x; })
                    .attr('y1', function(d) { return d.source.y; })
                    .attr('x2', function(d) { return d.target.x; })
                    .attr('y2', function(d) { return d.target.y; });

                nodes
                    .attr('cx', function(d) { return d.x; })
                    .attr('cy', function(d) { return d.y; });

                labels
                    .attr('x', function(d) { return d.x; })
                    .attr('y', function(d) { return d.y; });

                // Position edge labels at midpoint.
                linkGroup.selectAll('text')
                    .attr('x', function(d) { return (d.source.x + d.target.x) / 2; })
                    .attr('y', function(d) { return (d.source.y + d.target.y) / 2; });
            });

        initialized = true;
    }

    function updateTopology() {
        // On refresh, just reinitialize — the simulation stabilizes quickly.
        initTopology();
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

    // Initialize on page load.
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initTopology);
    } else {
        // DOMContentLoaded already fired (deferred script).
        setTimeout(initTopology, 0);
    }

    // Re-initialize after HTMX swap.
    document.body.addEventListener('htmx:afterSettle', function(e) {
        if (e.detail.target && e.detail.target.id === 'swarm-viz-envoys') {
            updateTopology();
        }
    });
})();
