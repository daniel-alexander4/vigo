# Demo Video Script — Silent with Subtitles

**Format:** Screen recording only, no voice, white subtitles on semi-transparent dark bar at bottom. Calm background music (lo-fi or ambient). ~3 minutes.

**Tools:** OBS for recording, DaVinci Resolve or Kdenlive for subtitles. Record at 1920x1080.

**Prep before recording:**
- Clean browser (incognito, no extensions, no bookmarks bar)
- Terminal with Catppuccin Mocha theme, large font (16-18pt)
- Vigo server running, 4 envoys enrolled and converging
- One envoy intentionally drifting (e.g., delete a file that a module manages)
- Have a simple module ready to publish (e.g., install htop + set MOTD)

---

## Shot List

### 1. Title Card (5s)

Black screen, fade in:

```
Vigo
Configuration management that converges in 15 seconds.
```

---

### 2. The Dashboard (10s)

**Action:** Open browser to Vigo dashboard at https://localhost:8443

**Subtitle:** `Four servers. All converged. This is the Vigo dashboard.`

Pause on dashboard — let viewer see the stat cards, fleet status, convergence chart.

---

### 3. Show a Module (15s)

**Action:** Switch to terminal. Open a module file:

```
cat /srv/vigo/stockpile/modules/motd.vgo
```

**Subtitle:** `Modules are plain YAML. No DSL to learn.`

Pause on the YAML for 3-4 seconds. Then:

**Subtitle:** `This module manages the message of the day on every server.`

---

### 4. Break Something (10s)

**Action:** SSH to one of the envoys and delete the managed file:

```
ssh web01
sudo rm /etc/motd
exit
```

**Subtitle:** `Let's delete the file it manages and see what happens.`

---

### 5. Watch It Heal (15s)

**Action:** Switch to the Vigo UI. Open the envoy detail page for web01. Show the drift detection.

**Subtitle:** `The agent detects drift within 15 seconds.`

Wait for the next check-in cycle. The envoy shows "relapsed" then converges back.

**Subtitle:** `And fixes it automatically. No human intervention.`

---

### 6. Publish a Change (20s)

**Action:** Switch to terminal. Edit a module to add a package:

```
vigocli config edit motd
```

Add a line (or show a pre-edited file). Then publish:

```
vigocli config publish
```

**Subtitle:** `Add a change. Publish. Every server picks it up on the next check-in.`

Switch to the UI — show the envoys converging with the new config.

**Subtitle:** `15 seconds later, the entire fleet is updated.`

---

### 7. Compliance Dashboard (10s)

**Action:** Click "Compliance" in the sidebar.

**Subtitle:** `21 compliance frameworks built in. HIPAA, SOC 2, PCI DSS, CIS, NIST, and more.`

Click into one framework to show per-envoy coverage.

**Subtitle:** `Per-server coverage. Which controls are met, which aren't, and why.`

---

### 8. Risk Scoring (8s)

**Action:** Click "Risk Posture" in the sidebar.

**Subtitle:** `Per-machine risk scores from CVEs, hardening audits, and convergence status.`

Pause on the risk page.

---

### 9. Remote Access (12s)

**Action:** Click an envoy, open a Scrier SSH session from the browser.

**Subtitle:** `Browser-based SSH. No VPN, no jump host, no port forwarding.`

Type `whoami` and `uptime` in the terminal to prove it's real.

**Subtitle:** `Full terminal. Through the agent's encrypted tunnel.`

Close the session.

---

### 10. Fleet Query (10s)

**Action:** Switch to terminal:

```
vigocli query "uptime.seconds" --target "*"
```

**Subtitle:** `Query any trait across the entire fleet in one command.`

Show the results (each hostname with its uptime).

---

### 11. Closing (10s)

**Action:** Switch back to the dashboard.

**Subtitle:** `Plain YAML. 15-second convergence. 8 platforms. 72 resource types.`

Fade to:

```
vigo.alexander4.org

Early access partner program — 100+ nodes, free unlimited license.
dan@alexander4.org
```

Hold for 5 seconds. Fade to black.

---

## Total Runtime

~2 minutes 5 seconds of content + transitions = ~2:30 final cut.

## Editing Notes

- **Pacing:** Hold each shot 1-2 seconds longer than feels natural. Viewers need time to read subtitles AND look at the screen.
- **Subtitles:** Max 2 lines, max 15 words per subtitle. White text, semi-transparent black background bar. No animation.
- **Transitions:** Simple cuts or very short crossfades (0.3s). No fancy transitions.
- **Speed up:** If a check-in takes 15 seconds of waiting, speed up the footage 3-4x with a subtle timer overlay showing real elapsed time.
- **Music:** Something like "Weightless" by Marconi Union or any royalty-free ambient track. Low volume — it's background, not foreground.
- **Resolution:** Record at 1080p. Upload at 1080p. YouTube, Vimeo, or self-hosted on the marketing site.
