// Module dependency DAG — D3 force-directed graph.
// Reads data from <script id="dependency-dag-data" type="application/json">.

(function() {
    var svg, simulation;

    // Theme-aware palettes. Mocha (dark) values match the rest of the UI's
    // dark-mode accents; Latte (light) values are the Catppuccin light
    // counterparts tuned for contrast on a pale background.
    var palettes = {
        dark: {
            edgeColors: {
                depends_on:   '#89b4fa', // Mocha Blue
                before:       '#cba6f7', // Mocha Mauve
                notify:       '#a6e3a1', // Mocha Green
                subscribes:   '#f9e2af', // Mocha Yellow
                watch_secret: '#fab387'  // Mocha Peach
            },
            edgeLabel:  '#9399b2',
            nodeModule: '#89b4fa',
            nodeDefault:'#a6e3a1',
            nodeStroke: '#1e1e2e',
            label:      '#cdd6f4'
        },
        light: {
            edgeColors: {
                depends_on:   '#1e66f5', // Latte Blue
                before:       '#8839ef', // Latte Mauve
                notify:       '#40a02b', // Latte Green
                subscribes:   '#df8e1d', // Latte Yellow
                watch_secret: '#fe640b'  // Latte Peach
            },
            edgeLabel:  '#6c6f85',
            nodeModule: '#1e66f5',
            nodeDefault:'#40a02b',
            nodeStroke: '#eff1f5',
            label:      '#4c4f69'
        }
    };

    function activePalette() {
        return document.documentElement.classList.contains('theme-dark') ? palettes.dark : palettes.light;
    }

    var edgeDash = {
        depends_on: '',
        before:     '6,3',
        notify:     '3,3',
        subscribes: '3,3',
        watch_secret: '6,3'
    };

    function readData() {
        var el = document.getElementById('dependency-dag-data');
        if (!el) return null;
        try { return JSON.parse(el.textContent); } catch(e) { return null; }
    }

    function initDAG() {
        var container = document.getElementById('dependency-dag-container');
        if (!container) return;
        var data = readData();
        if (!data || !data.nodes || data.nodes.length === 0) return;

        var width = container.clientWidth || 600;
        var height = container.clientHeight || 400;

        container.innerHTML = '';

        svg = d3.select(container).append('svg')
            .attr('width', width)
            .attr('height', height);

        var p = activePalette();

        // Arrow markers per edge type.
        var defs = svg.append('defs');
        Object.keys(p.edgeColors).forEach(function(type) {
            defs.append('marker')
                .attr('id', 'arrow-' + type)
                .attr('viewBox', '0 -5 10 10')
                .attr('refX', 22)
                .attr('refY', 0)
                .attr('markerWidth', 6)
                .attr('markerHeight', 6)
                .attr('orient', 'auto')
                .append('path')
                .attr('d', 'M0,-5L10,0L0,5')
                .attr('fill', p.edgeColors[type]);
        });

        var linkGroup = svg.append('g');
        var nodeGroup = svg.append('g');
        var labelGroup = svg.append('g');

        // Links.
        var links = linkGroup.selectAll('line')
            .data(data.edges)
            .join('line')
            .attr('stroke', function(d) { return p.edgeColors[d.type] || p.edgeLabel; })
            .attr('stroke-width', 2)
            .attr('stroke-opacity', 0.7)
            .attr('stroke-dasharray', function(d) { return edgeDash[d.type] || ''; })
            .attr('marker-end', function(d) { return 'url(#arrow-' + d.type + ')'; });

        // Edge labels.
        var edgeLabels = linkGroup.selectAll('text')
            .data(data.edges)
            .join('text')
            .attr('font-size', 8)
            .attr('fill', p.edgeLabel)
            .attr('text-anchor', 'middle')
            .text(function(d) { return d.type; });

        // Nodes.
        var nodes = nodeGroup.selectAll('circle')
            .data(data.nodes, function(d) { return d.id; })
            .join('circle')
            .attr('r', function(d) { return d.type === 'module' ? 12 : 8; })
            .attr('fill', function(d) { return d.type === 'module' ? p.nodeModule : p.nodeDefault; })
            .attr('stroke', p.nodeStroke)
            .attr('stroke-width', 1.5)
            .call(d3.drag()
                .on('start', dragStarted)
                .on('drag', dragged)
                .on('end', dragEnded));

        nodes.append('title')
            .text(function(d) { return d.id + ' (' + d.type + ')'; });

        // Labels.
        var labels = labelGroup.selectAll('text')
            .data(data.nodes, function(d) { return d.id; })
            .join('text')
            .attr('font-size', 10)
            .attr('font-family', 'monospace')
            .attr('fill', p.label)
            .attr('text-anchor', 'middle')
            .attr('dy', -16)
            .text(function(d) { return d.id; });

        // Force simulation.
        simulation = d3.forceSimulation(data.nodes)
            .force('link', d3.forceLink(data.edges).id(function(d) { return d.id; }).distance(100))
            .force('charge', d3.forceManyBody().strength(-250))
            .force('center', d3.forceCenter(width / 2, height / 2))
            .force('collision', d3.forceCollide().radius(25))
            .on('tick', function() {
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

                edgeLabels
                    .attr('x', function(d) { return (d.source.x + d.target.x) / 2; })
                    .attr('y', function(d) { return (d.source.y + d.target.y) / 2 - 6; });
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
        document.addEventListener('DOMContentLoaded', initDAG);
    } else {
        setTimeout(initDAG, 0);
    }

    document.body.addEventListener('htmx:afterSettle', function(e) {
        if (e.detail.target && e.detail.target.id === 'dependency-dag-container') {
            initDAG();
        }
    });

    // Arrow markers reference a single palette at <defs> creation time, so a
    // full re-render is the cleanest way to re-theme the DAG.
    document.addEventListener('theme-changed', function() {
        if (document.getElementById('dependency-dag-container')) {
            initDAG();
        }
    });
})();
