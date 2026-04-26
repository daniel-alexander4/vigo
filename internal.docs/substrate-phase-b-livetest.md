# Substrate unification — live test plan (0.30.17)

*Written 2026-04-24 EOD. Run on the home lab tomorrow before piling more agent work on top of 0.30.17.*

**Automated runner:** `substrate-phase-b-deploy.sh` (section 0) and `substrate-phase-b-livetest.sh` (sections 1–7). The livetest script is run-all-and-report — every assertion runs independently and a summary prints at the end. Sections 4–7 require `~/.sigil-test-password` (mode 0600) for non-interactive `vigo sigil enroll` / `unlock` (works via piped stdin — the agent's `read_one_password` already auto-detects TTY vs pipe at `agent/src/sigil/cli.rs:1201`). The manual steps below remain authoritative for diagnosis when an assertion fails.

Three envoys: danlap = primary, annlap + plex = peers, girlslaptop = optional for evict tests (it has a known sigil enrollment gap, which is itself useful for the "host not in fleet projection" error path).

## 0. Build + deploy

```bash
cd /home/dan/cairn/vigo
make dist-agent

for h in danlap annlap plex; do
  scp dist/agent/linux/amd64/vigo $h:/tmp/vigo
  ssh $h 'sudo install -m 0755 /tmp/vigo /usr/local/sbin/vigo && sudo systemctl restart vigo-envoy'
done

for h in danlap annlap plex; do ssh $h 'vigo --version'; done
```

For arm64 envoys, swap `dist/agent/linux/amd64/vigo` → `dist/agent/linux/arm64/vigo`.

## 1. Phase A — cairn source CLI (on danlap as `dan`)

```bash
mkdir -p ~/cairn/sourcetest && cd ~/cairn/sourcetest && git init -q && echo x > x && git add . && git commit -qm init

vigo cairn source add ~/cairn/sourcetest --tags smoke --description "phase A test"
# Expect: ✓ wrote, ✓ appended .cairn.yaml

cat ~/cairn/sourcetest/.gitignore        # contains .cairn.yaml
vigo cairn source show sourcetest        # owner=danlap, max_mirrors=5, paused=false

vigo cairn source pause sourcetest
sleep 6 && vigo cairn source list        # PAUSED=yes for sourcetest
vigo cairn source resume sourcetest

vigo cairn source validate ~/cairn/sourcetest/.cairn.yaml
echo 'garbage: yes' > /tmp/bad.yaml
vigo cairn source validate /tmp/bad.yaml   # non-zero, readable error

vigo cairn source remove sourcetest --yes
ls ~/cairn/sourcetest/.cairn.yaml 2>&1     # no such file
```

## 2. Phase B — `vigo cairn leave` (on danlap as `dan`)

```bash
for n in leave1 leave2; do
  mkdir -p ~/cairn/$n && cd ~/cairn/$n && git init -q && echo $n > x && git add . && git commit -qm $n
  vigo cairn source add ~/cairn/$n --no-gitignore
done

# --pause variant
vigo cairn leave --pause --yes
grep ^paused ~/cairn/leave*/.cairn.yaml    # both: paused: true

vigo cairn source resume leave1

# Plain leave (delete)
vigo cairn leave --yes
ls ~/cairn/leave*/.cairn.yaml 2>&1         # no such file

rm -rf ~/cairn/leave1 ~/cairn/leave2
```

## 3. Phase B — `vigo cairn prune --seeder` + `vigocli cairn evict`

```bash
ssh annlap 'vigo cairn list --seeder danlap'

ssh annlap 'sudo vigo cairn prune --seeder danlap'             # lists candidates, exits
ssh annlap 'sudo vigo cairn prune --seeder danlap --yes'       # actually prunes

vigocli cairn members
vigocli cairn evict --host girlslaptop --yes
sleep 10 && vigocli cairn status
```

## 4. Phase B — `vigo sigil leave` (on annlap as `dan`)

```bash
vigo sigil status
echo phase-b > /tmp/test.txt && vigo sigil encrypt /tmp/test.txt --as smoketest.age
ssh danlap 'cat ~/.sigil/recipients.txt' | wc -l               # N pubkeys (incl annlap)

vigo sigil leave --yes
ls ~/.sigil/leave.pending                                      # sentinel exists

sudo journalctl -u vigo-envoy -n 30 -f &
sleep 35
kill %1 2>/dev/null

ls -la ~/.sigil/ 2>&1                                          # gone (default scrub)
ls -la ~/sigil/                                                # ciphertext kept (orphaned)

ssh danlap 'cat ~/.sigil/recipients.txt' | wc -l               # N-1 pubkeys
ssh danlap 'ls ~/.sigil/reencrypt.pending'                     # sentinel exists
ssh danlap 'vigo sigil unlock && ls ~/.sigil/reencrypt.pending 2>&1'  # gone after drain

# Restore baseline
vigo sigil enroll
```

## 5. Phase B — `vigocli sigil evict` (server host)

```bash
vigocli sigil members dan

vigocli sigil evict --host plex --user dan --yes
sleep 5
ssh danlap 'cat ~/.sigil/recipients.txt' | grep plex || echo "plex pubkey gone ✓"

vigocli sigil evict --host girlslaptop --all-users --yes

ssh plex 'vigo sigil enroll'
```

## 6. `--purge` variant on sigil leave

```bash
vigo sigil enroll
echo secret > /tmp/s.txt && vigo sigil encrypt /tmp/s.txt
ls ~/sigil/

vigo sigil leave --purge --yes
sleep 35
ls ~/.sigil/ ~/sigil/ 2>&1                                     # both gone
```

## 7. Regression (DELETE tombstone path unchanged)

```bash
echo regress > /tmp/r.txt
vigo sigil encrypt /tmp/r.txt --as regress.age
vigo sigil delete regress.age
sleep 35
ssh danlap 'vigo sigil list --tombstones | grep regress.age'   # tombstone propagated
```

## Likely failure modes to flag (don't fix mid-test — note them)

- **`leave.pending` never clears** → daemon didn't pick up the sentinel. `sudo journalctl -u vigo-envoy | grep -i sigil` for errors. Likely `home_for_user` returning None or `load_local_signing_key` failing.
- **`recipients.txt` doesn't shrink on peers** → LEAVE envelope didn't reach them or `apply_leave` raced. Peer's journal will say `LEAVE rejected` (sig fail) or `LEAVE accepted`.
- **`vigo sigil leave` hangs** → CLI is just `fs::write` + print; sub-second. A hang means something external.
- **`vigocli sigil evict` returns "host is not enrolled in any matching sigil circle"** → fleet projection gap. Recent enrollment that hasn't reported yet, or the girlslaptop enrollment gap.
- **0644 mode on regenerated `recipients.txt`** → catches the pending sigil-encrypt mode bug as a side effect. Flag it; don't fix in this test session.

## What's NOT being tested here (deferred)

- Digest-gossip catch-up for an offline peer that misses the live LEAVE fan-out (Phase B.1).
- Re-enroll-after-leave with a fresh age identity — verifying the LEAVE record's pubkey-keyed monotonicity prevents replay against the reborn host.
- Cross-subnet LEAVE delivery (only matters if `cross_subnet_peers:` is configured).
