# Use Cases & Performance

## Use Cases

- [Core Use Cases](use-cases.md) — Configuration management, security enforcement, network automation, incident response, observability — tiered by recommended check-in interval

## Performance (Theoretical)

Scaling projections on 4 vCPU / 8 GB / SSD hardware at different check-in intervals. These are theoretical projections based on benchmarked per-request costs from the codebase, not measured end-to-end under production load. Actual performance will vary with hardware, network conditions, policy complexity, and fleet composition.

- [1-Minute Check-ins](performance-1m.md) — Per-request cost derivations and baseline projections
- [30-Second Check-ins](performance-30s.md) — Recommended for responsive fleets (up to 15k thralls)
- [15-Second Check-ins](performance-15s.md) — Near-real-time state enforcement (up to 10k thralls)
