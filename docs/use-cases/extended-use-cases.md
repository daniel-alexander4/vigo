# Extended Use Cases

Beyond the [core use cases](use-cases.md), Hobnob's architecture — lightweight agent, near-real-time trait collection, idempotent state enforcement, offline convergence, authenticated channels — maps onto domains that don't look like traditional IT infrastructure at first glance.

The pattern is the same everywhere: **more than a handful of networked things that need to be in a known state, and someone who cares whether they are.**

## Live Event Production

A touring concert has 200+ networked devices per venue: lighting rigs, sound processors, video walls, fog machines, pyrotechnics controllers. The show designer defines desired state for each song in the setlist. Between songs, publish the next configuration. At 15-second check-ins, every device converges before the drummer counts in.

Traits report firmware versions, lamp temperatures, lamp hours remaining, signal levels, and power draw. A rig that goes stale mid-show is flagged before the audience notices. Hub-spoke maps to stage-left, stage-right, front-of-house, and monitors. Offline convergence means if the network switch dies mid-show, every device keeps running its last-known lighting cue.

**Recommended interval:** 15 seconds. Cue transitions between songs are time-critical. The fleet is small (200-500 devices), so load is trivial.

## Election Infrastructure

Every voting machine, tabulation server, and election management workstation in a county is a thrall. Desired state: correct ballot definitions loaded, USB ports disabled, firewall locked to whitelist, audit logging enabled, correct time source configured.

Traits report serial numbers, firmware versions, tamper-evident seal status, and network connectivity. At 30-second intervals, a poll worker unplugging a machine from the network triggers a stale alert within 90 seconds. The compliance dashboard shows election officials exactly which machines are configured correctly, in real time, during voting hours. The compliance export (OSCAL) feeds directly into EAC audit requirements.

**Recommended interval:** 30 seconds. Continuous compliance visibility is critical for public trust. Stale detection within 90 seconds catches tampering or equipment failure during voting hours.

## Hospital Medical Device Fleets

Thousands of infusion pumps, patient monitors, and diagnostic instruments across a hospital network. Desired state: correct drug library version loaded on infusion pumps, correct alarm thresholds on monitors, current firmware, WiFi config pointing to the right SSID, NTP synchronized.

Traits report battery level, WiFi signal strength, medication library version, last calibration date, and error counts. At 30-second intervals, a pump that disconnects from WiFi is flagged within 90 seconds — before a nurse notices the gap. `when: "battery_percent < 20"` triggers a low-battery alert. Spanner maps to hospital wings or campuses.

**Recommended interval:** 30 seconds. Medical device connectivity is a patient safety issue. Fast stale detection catches devices that have fallen off the network.

## Autonomous Vehicle Ground Stations

Every vehicle in a self-driving fleet has a ground-side twin — a compute node that manages map updates, ML model versions, geofence policies, and operational parameters. Desired state defines which model version to deploy, which geofence rules to enforce, and speed limits by zone.

At 15-second intervals, the operations team publishes an emergency geofence (construction zone, accident scene) and every ground station converges within 15 seconds, pushing the update to vehicles on their next sync. Traits report vehicle location, model version, last successful update, and battery/fuel level.

**Recommended interval:** 15 seconds. Emergency geofence updates are time-critical for public safety.

## Trading Floor Workstations

500 workstations at a financial firm. Regulatory compliance requires specific configurations: screen lock timeouts, approved application versions, DLP agent running, USB storage disabled, correct market data feed URLs.

At 30-second intervals, compliance is continuous — not a quarterly audit but a live dashboard. A trader installing unauthorized software triggers drift detection within 30 seconds. Task dispatch: "rotate API keys for market data feed" across all workstations simultaneously during a credential exposure incident.

**Recommended interval:** 30 seconds. Regulatory compliance requires demonstrable continuous monitoring. Incident response (credential rotation) benefits from fast propagation.

## Smart Building Management

Every HVAC controller, lighting panel, access badge reader, elevator controller, and fire alarm panel in a commercial building is a thrall. Desired state: HVAC setpoints by zone and time of day, lighting schedules, access control lists per door, elevator firmware versions.

Traits report room temperature, humidity, occupancy sensor readings, door open/closed status, and energy consumption per zone. At 30-second intervals, the building manager sees a near-real-time environmental heatmap. `when: "occupancy == 0 && time_after('18:00')"` enforces energy savings mode. Hub-spoke maps to floors or buildings in a campus.

**Recommended interval:** 30 seconds. Occupancy-responsive energy management needs fast trait updates. Access control changes (badge revocation) should propagate quickly.

## Drone Swarm Ground Control

A fleet of agricultural survey drones, each with a ground-side controller. Desired state: flight plan parameters, camera settings, no-fly zone geofences, return-to-home altitude, radio frequencies.

Traits report GPS coordinates, battery voltage, wind speed at altitude, photos taken, and storage remaining. At 15-second intervals, the operator publishes an updated no-fly zone (a crop duster just entered the airspace) and every controller receives it within 15 seconds. Offline convergence means if the ground station loses cellular, drones continue their last-known mission plan.

**Recommended interval:** 15 seconds. Airspace safety updates (no-fly zones) are time-critical.

## Cruise Ship IT

A floating city with 3,000 crew devices, 1,000 passenger-facing kiosks, POS terminals in 20 restaurants, entertainment systems in 2,000 cabins, navigation and safety systems on the bridge.

Hub-spoke maps to decks or departments. Offline convergence is critical — satellite internet is slow, expensive, and intermittent. The ship's local Hobnob server enforces state regardless of shore connectivity. At 30-second intervals, the IT officer sees the entire vessel's device fleet in real time. Port days trigger a config publish — different regulatory requirements apply in different countries' territorial waters.

**Recommended interval:** 30 seconds. The fleet is large but self-contained. Regulatory config changes on port arrival need fast propagation. Offline convergence is the killer feature for maritime operations.

## Cryptocurrency Mining Farms

Thousands of mining rigs, each needing specific overclocking profiles, pool configurations, firmware versions, and fan curves. Traits report hash rate, GPU temperature, power consumption, error rates, and uptime.

At 15-second intervals, a rig that drops offline or shows anomalous temperatures is flagged within 45 seconds. `when: "gpu_temp > 85"` triggers a power-limit reduction automatically. Config publish: switch the entire farm from one mining pool to another based on profitability — and watch the hash rates change in real time on the dashboard.

**Recommended interval:** 15 seconds. Thermal protection is time-sensitive (GPUs can throttle or fail within seconds of overheating). Pool switching profitability is measured in minutes.

## Antarctic & Remote Research Stations

A handful of research stations with dozens of scientific instruments, weather stations, satellite uplinks, and life-safety systems. Offline convergence is the killer feature — internet connectivity is via intermittent satellite passes, sometimes only a few hours per day.

The agent caches policy and converges locally during the 20+ hours with no connectivity. When the satellite window opens, pending results drain and traits update. The remote operations center in Christchurch (or Houston, or wherever) sees the station's status update in real time during connected windows. At 30-second intervals during those windows, they verify every instrument and safety system is healthy before the next blackout.

**Recommended interval:** 30 seconds during connected windows. The interval is irrelevant during offline periods — offline convergence handles enforcement regardless.

## Escape Room Automation

Each room has 20-40 networked props: electromagnetic locks, LED strips, sound effects modules, video displays, sensors, and mechanical releases. The game master defines desired state per puzzle stage. When players solve a puzzle, the GM publishes the next stage and every prop converges within 15 seconds — lights change, new audio plays, a hidden door unlocks.

Traits report prop health, sensor status, and player progress indicators. Between sessions, publish "reset" config and watch the dashboard as every prop returns to starting state. The 5 MB agent runs on the Raspberry Pis and ESP32s inside each prop.

**Recommended interval:** 15 seconds. Puzzle state transitions need to feel instant to players. The fleet is tiny (20-40 devices per room), so load is negligible even at aggressive intervals.

## Satellite Ground Station Networks

Dozens of antenna installations tracking satellites across multiple continents. Desired state: antenna pointing parameters, frequency allocations, recording schedules, firmware versions, security configurations.

Each ground station operates semi-autonomously (offline convergence handles connectivity gaps between sites). At 30-second intervals during connected periods, the operations center knows exactly which antennas are tracking which satellites, storage utilization, link quality, and equipment health. Emergency retasking — redirect antennas to track a debris event or a satellite anomaly — propagates within 30 seconds.

**Recommended interval:** 30 seconds. Emergency retasking is time-sensitive. Stations may have intermittent connectivity, making offline convergence essential.

## Vending Machine & Kiosk Networks

Thousands of vending machines, parking meters, or self-service kiosks across a city or region. Desired state: pricing tables, accepted payment methods, display content, firmware versions, VPN configuration.

Traits report inventory levels, last transaction time, coin box status, connectivity quality, and ambient temperature. At 30-second intervals, a machine that stops reporting is flagged within 90 seconds — dispatch a technician before customers start complaining. `when: "inventory_percent < 10"` triggers a restock alert. Hub-spoke maps to geographic regions.

**Recommended interval:** 30 seconds. Stale detection catches offline machines quickly. The fleet is large and geographically distributed, making centralized visibility essential.

## Interval Recommendations Summary

| Use Case | Interval | Why |
|----------|----------|-----|
| Live event production | 15s | Cue transitions are time-critical |
| Escape room automation | 15s | Puzzle state must feel instant |
| Autonomous vehicle ground stations | 15s | Emergency geofences are safety-critical |
| Cryptocurrency mining | 15s | Thermal protection is time-sensitive |
| Drone swarm ground control | 15s | Airspace safety updates are time-critical |
| Election infrastructure | 30s | Continuous compliance visibility during voting |
| Hospital medical devices | 30s | Device connectivity is a patient safety issue |
| Trading floor workstations | 30s | Regulatory continuous monitoring requirement |
| Smart building management | 30s | Occupancy-responsive energy and access control |
| Cruise ship IT | 30s | Regulatory config on port arrival, offline resilience |
| Satellite ground stations | 30s | Emergency retasking, intermittent connectivity |
| Vending/kiosk networks | 30s | Stale detection for geographically dispersed fleets |
| Antarctic research stations | 30s | Maximize visibility during satellite windows |
