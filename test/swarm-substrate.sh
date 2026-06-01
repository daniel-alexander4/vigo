#!/usr/bin/env bash
# Copyright (c) 2026 Alexander4, LLC
# SPDX-License-Identifier: LicenseRef-Proprietary
#
# End-to-end swarm validation against a real Vigo fleet.
#
# Exercises all six swarm content subsystems — filecast(payload),
# longdrawer, lockbox, gitback, curator, poolq — plus the puddle
# identity lifecycle, in sequence. Pass/fail per step, exit non-zero
# on any failure. Mirrors the manual procedure captured in
# tshoot/log-substrate-2026-05-09.md so a future "did everything
# still work" check is one command.
#
# Usage:
#   test/swarm-substrate.sh [--wipe]
#
# Env overrides:
#   VIGO_FLEET=<host,host,host>      (default: plex,girlslaptop,annlap)
#   VIGO_FOUNDER=<host>              (default: first entry of VIGO_FLEET)
#   VIGO_TEST_PASSPHRASE=<phrase>    (default: test-substrate)
#
# Behavior:
#   --wipe is required and destructive. It is a COMPLETE swarm reset:
#   first it revokes every filecast distribution server-side (otherwise
#   agents re-materialize the files after the scrub); then on each envoy
#   it stops the agent, removes the whole root-owned /var/lib/vigo/swarm
#   + /var/lib/vigo/artifacts and every per-user swarm dir (current names
#   AND retired lineages — cryptsync/vigocrypt, swarmgit/gitmirror/
#   vigorepo/cairn, filesync/vigosync, longdrawer), then restarts the
#   agent. It preserves /var/lib/vigo/{state,security} (agent enrollment)
#   and never touches ~/repos, user documents, or /srv/vigo. Then it runs
#   the full 7-step sequence (payload → longdrawer → puddle → lockbox →
#   gitback → curator → poolq) from a genuinely clean slate.
#
# Why no non-destructive mode: gitback's project_id is fixed by the
# founder's puddle pubkey, so a single founder can only have ONE
# project at a time. A "quick smoke" mode that re-inits would
# always collide with the prior run's project. Use the
# `cargo test --test gitback_helper` integration test for a
# fleet-free smoke; this script is for full multi-envoy validation.
#
# Operator quirks the runlog flagged:
#   - non-interactive ssh sessions don't inherit XDG_RUNTIME_DIR; this
#     script exports it explicitly before every vigo verb.
#   - puddle/lockbox session daemons exit when their parent bash
#     dies; this script keeps each unlock + verb chain in one ssh.
#   - linger must be enabled per-user (see step pre/wipe). Idempotent.

set -euo pipefail

# ---------- arg parsing ----------

WIPE=0
for arg in "$@"; do
    case "$arg" in
        --wipe) WIPE=1 ;;
        --help|-h)
            sed -n '1,40p' "$0" | sed 's/^# \?//'
            exit 0
            ;;
        *)
            echo "unknown arg: $arg" >&2
            echo "usage: $0 --wipe" >&2
            exit 64
            ;;
    esac
done

if [[ $WIPE -ne 1 ]]; then
    cat >&2 <<'EOF'
swarm-substrate.sh requires --wipe.

This script is destructive: it revokes all filecast distributions
server-side, then on every envoy removes the whole /var/lib/vigo/swarm
+ /var/lib/vigo/artifacts and every per-user swarm dir (current names +
retired lineages), preserving only agent enrollment, before running the
full 7-step substrate test from a genuinely clean slate. It never
touches ~/repos, user documents, or /srv/vigo. There is no
non-destructive mode (gitback's single-project-per-puddle invariant
precludes a re-runnable smoke).

For a fleet-free quick smoke, run:
    cd agent && cargo test --test gitback_helper

To run this script anyway:
    test/swarm-substrate.sh --wipe
EOF
    exit 64
fi

FLEET="${VIGO_FLEET:-plex,girlslaptop,annlap}"
PASSPHRASE="${VIGO_TEST_PASSPHRASE:-test-substrate}"
IFS=',' read -ra HOSTS <<< "$FLEET"
FOUNDER="${VIGO_FOUNDER:-${HOSTS[0]}}"
RECEIVERS=()
for h in "${HOSTS[@]}"; do
    [[ "$h" != "$FOUNDER" ]] && RECEIVERS+=("$h")
done

TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
TAG="substrate-$TIMESTAMP"

# ---------- helpers ----------

PASS=0
FAIL=0
LAST_STEP=""

step() {
    echo
    echo "=== $1 ==="
    LAST_STEP="$1"
}

ok() {
    echo "[ok] $LAST_STEP — $*"
    PASS=$((PASS+1))
}

bad() {
    echo "[FAIL] $LAST_STEP — $*" >&2
    FAIL=$((FAIL+1))
    return 1
}

# Run a command on a remote host with XDG_RUNTIME_DIR set so vigo
# session daemons + verbs find each other on the canonical socket.
remote_run() {
    local host=$1; shift
    ssh "$host" "export XDG_RUNTIME_DIR=/run/user/\$(id -u); $*"
}

# Unlock the puddle on the host. linger must already be enabled (we
# do that once in pre-flight). The unlock + any subsequent verbs
# share one ssh so the session daemon stays alive.
remote_unlock_and() {
    local host=$1; shift
    ssh "$host" "
        set -e
        export XDG_RUNTIME_DIR=/run/user/\$(id -u)
        echo '$PASSPHRASE' | vigo swarm puddle unlock > /dev/null 2>&1
        sleep 1
        $*
    "
}

# ---------- step 1: pre-flight ----------

step "pre-flight: probe vigosrv"

# A reachable server is a precondition for any agent to come up after
# wipe — without it, the post-wipe `systemctl start vigo-envoy` lands
# in an auto-restart-loop on connection-refused. Hit the REST API
# (vigocli's transport) to fail fast here rather than mid-wipe.
if ! sudo vigocli doctor >/dev/null 2>&1; then
    bad "vigocli doctor failed — is vigosrv reachable on https://localhost:8443?"
    exit 1
fi
ok "vigosrv reachable"

step "pre-flight: probe fleet"

for h in "${HOSTS[@]}"; do
    if ! ssh -o ConnectTimeout=5 -o BatchMode=yes "$h" 'true' 2>/dev/null; then
        bad "ssh to $h failed (or non-batch-mode)"
        exit 1
    fi
    v=$(ssh "$h" 'vigo version 2>&1' || true)
    echo "  $h vigo=$v"
done
ok "all ${#HOSTS[@]} envoys reachable"

step "pre-flight: enable linger"
for h in "${HOSTS[@]}"; do
    ssh "$h" 'sudo loginctl enable-linger $(id -un) 2>/dev/null || true'
done
ok "linger enabled (idempotent)"

# ---------- step 2: wipe (optional) ----------

if [[ $WIPE -eq 1 ]]; then
    # Filecast is the one subsystem whose state lives in the SERVER
    # manifest, not the agent — so a per-host scrub alone is undone the
    # moment agents re-materialize on their next check-in. Revoke every
    # distribution first (stop + evict). revoke needs the full sha256,
    # which the table truncates, so pull it from --json.
    step "wipe: revoke + purge all filecasts (server-side)"
    # Revoke every active distribution (stop + evict) so agents don't
    # re-materialize the files after the per-host scrub, then purge the
    # revoked tombstones so the manifest is genuinely empty (no stale
    # rows in `list`/the UI). revoke needs the full sha256, which the
    # table truncates — pull it from --json.
    revoked=0
    while read -r sha; do
        [ -n "$sha" ] || continue
        sudo vigocli swarm filecast revoke "$sha" >/dev/null 2>&1 && revoked=$((revoked+1))
    done < <(sudo vigocli swarm filecast list --json 2>/dev/null | grep -oE '[0-9a-f]{64}' | sort -u)
    sudo vigocli swarm filecast purge >/dev/null 2>&1 || true
    ok "revoked $revoked + purged filecast tombstones"

    step "wipe: stop agent + scrub ALL swarm state on each envoy"
    for h in "${HOSTS[@]}"; do
        ssh "$h" '
            set -e
            sudo systemctl stop vigo-envoy

            # Full clean slate. sudo throughout: curator/gitback root helper
            # daemons write root-owned files into the per-user dirs, so a
            # plain user rm hits EPERM. We scrub only the swarm subtrees of
            # /var/lib/vigo (NOT state/ or security/ — that is agent
            # enrollment/identity, which must survive). The per-user list is
            # fixed dir names (current + retired lineages: cryptsync/vigocrypt
            # = lockbox, swarmgit/gitmirror/vigorepo/cairn = gitback,
            # filesync/vigosync = longdrawer) — never repos or user documents.
            #
            # Empty swarm/ + artifacts/ by CONTENT, not the dirs themselves:
            # on a server host these dirs are bind-mounted into the vigosrv
            # container, and rm-ing the dir deletes the mount source inode
            # (the container is then stuck with a stale, empty view). Removing
            # only the contents keeps the inode + bind mount intact.
            for sd in /var/lib/vigo/swarm /var/lib/vigo/artifacts; do
                if [ -d "$sd" ]; then sudo find "$sd" -mindepth 1 -maxdepth 1 -exec rm -rf {} +; fi
            done

            USER_DIRS=".vigo-puddle .vigo-gitback .vigo-curator .vigo-poolq .lockbox lockbox longdrawer .cryptsync .cryptsync-test-password .vigocrypt cryptsync .vigo-swarmgit .vigo-gitmirror .vigorepo .vigo-cairn filesync vigosync longdrawer.old"
            for home in /home/* /root; do
                [ -d "$home" ] || continue
                for d in $USER_DIRS; do sudo rm -rf "$home/$d"; done
            done

            sudo systemctl start vigo-envoy
            # Poll for active — vigo-envoy can spend several seconds in
            # "activating" doing enrollment refresh + initial check-in.
            for i in $(seq 1 60); do
                state=$(systemctl is-active vigo-envoy 2>/dev/null || true)
                [ "$state" = "active" ] && exit 0
                sleep 1
            done
            echo "vigo-envoy did not reach active within 60s (state=$state)" >&2
            exit 1
        ' || { bad "wipe failed on $h"; exit 1; }
        echo "  $h wiped + agent restarted"
    done
    ok "swarm state cleared on ${#HOSTS[@]} envoys"
fi

# ---------- step 3: payload ----------

if true; then
    step "payload: distribute marker"
    MARKER=/tmp/$TAG-payload.txt
    echo "$TAG payload marker $(date -Iseconds)" > "$MARKER"
    SHA=$(sha256sum "$MARKER" | cut -d' ' -f1)
    sudo vigocli swarm filecast distribute "$MARKER" --target '*' --label "$TAG" --retention 1h >/dev/null
    echo "  seeded sha256=$SHA"

    deadline=$(( $(date +%s) + 60 ))
    while :; do
        ready=0
        for h in "${HOSTS[@]}"; do
            if ssh "$h" "sudo test -L /var/lib/vigo/swarm/files/$TAG" 2>/dev/null; then
                ready=$((ready+1))
            fi
        done
        echo "  ready: $ready/${#HOSTS[@]}"
        [[ $ready -eq ${#HOSTS[@]} ]] && break
        if [[ $(date +%s) -ge $deadline ]]; then
            bad "payload didn't materialize on all envoys within 60s"
            exit 1
        fi
        sleep 3
    done
    ok "payload materialized on ${#HOSTS[@]} envoys"
fi

# ---------- step 4: longdrawer ----------

if true; then
    step "longdrawer: drop on $FOUNDER, watch peers"
    for h in "${HOSTS[@]}"; do
        ssh "$h" 'mkdir -p ~/longdrawer'
    done
    ssh "$FOUNDER" "echo '$TAG longdrawer from $FOUNDER' > ~/longdrawer/$TAG.txt"

    deadline=$(( $(date +%s) + 60 ))
    while :; do
        ready=0
        for h in "${RECEIVERS[@]}"; do
            if ssh "$h" "test -f ~/longdrawer/$TAG.txt"; then
                ready=$((ready+1))
            fi
        done
        echo "  receivers ready: $ready/${#RECEIVERS[@]}"
        [[ $ready -eq ${#RECEIVERS[@]} ]] && break
        if [[ $(date +%s) -ge $deadline ]]; then
            bad "longdrawer didn't propagate to all receivers within 60s"
            exit 1
        fi
        sleep 3
    done
    ok "longdrawer propagated to ${#RECEIVERS[@]} receiver(s)"
fi

# ---------- step 5: puddle ----------

if true; then
    step "puddle: init founder, pair + join + unlock receivers"
    ssh "$FOUNDER" "
        set -e
        export XDG_RUNTIME_DIR=/run/user/\$(id -u)
        printf '%s\n%s\n' '$PASSPHRASE' '$PASSPHRASE' | vigo swarm puddle init >/dev/null
        echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null
    " || { bad "puddle init on $FOUNDER failed"; exit 1; }

    for h in "${RECEIVERS[@]}"; do
        CODE=$(ssh "$FOUNDER" 'vigo swarm puddle pair 2>&1' | awk '/^Pairing code:/{print $3}')
        if [[ -z "$CODE" ]]; then
            bad "no pair code from $FOUNDER"; exit 1
        fi
        ssh "$h" "
            set -e
            export XDG_RUNTIME_DIR=/run/user/\$(id -u)
            printf '%s\n%s\n' '$CODE' '$PASSPHRASE' | sudo -E vigo swarm puddle join --from $FOUNDER >/dev/null
            echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null
        " || { bad "puddle join on $h failed"; exit 1; }
        echo "  $h joined + unlocked"
    done
    ok "all ${#HOSTS[@]} envoys members of the same puddle"
fi

# ---------- step 6: lockbox ----------

if true; then
    step "lockbox: encrypt on $FOUNDER, decrypt on receivers"
    # Wait for recipients.txt to converge (3 entries on each).
    deadline=$(( $(date +%s) + 30 ))
    while :; do
        all_three=1
        for h in "${HOSTS[@]}"; do
            n=$(ssh "$h" 'wc -l < ~/.lockbox/recipients.txt 2>/dev/null' || echo 0)
            [[ "$n" -lt ${#HOSTS[@]} ]] && all_three=0
        done
        [[ $all_three -eq 1 ]] && break
        if [[ $(date +%s) -ge $deadline ]]; then
            bad "recipient-list convergence timed out at 30s"
            exit 1
        fi
        sleep 2
    done

    # Lock-then-drop-then-unlock so the unlock-time sweep handles the
    # encrypt + push. The watcher's auto-encrypt-on-write path encrypts
    # in place but doesn't emit a PushJob — only the sweep-on-unlock
    # path enqueues fan-out. Match the human flow that produces
    # propagation in <10 s.
    ssh "$FOUNDER" "
        export XDG_RUNTIME_DIR=/run/user/\$(id -u)
        vigo swarm puddle lock >/dev/null 2>&1 || true
        echo '$TAG lockbox from $FOUNDER' > ~/lockbox/$TAG.txt
        sleep 1
        echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null
        sleep 5
    " >/dev/null

    for h in "${RECEIVERS[@]}"; do
        # Wait for ciphertext to land.
        deadline=$(( $(date +%s) + 30 ))
        while :; do
            if ssh "$h" "test -f ~/lockbox/$TAG.txt"; then
                break
            fi
            if [[ $(date +%s) -ge $deadline ]]; then
                bad "ciphertext didn't reach $h within 30s"; exit 1
            fi
            sleep 2
        done
        # Decrypt and assert plaintext matches.
        plain=$(ssh "$h" "
            export XDG_RUNTIME_DIR=/run/user/\$(id -u)
            echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null 2>&1
            sleep 1
            vigo swarm lockbox decrypt $TAG.txt -o /tmp/$TAG-dec.txt >/dev/null 2>&1 && cat /tmp/$TAG-dec.txt
        ")
        expected="$TAG lockbox from $FOUNDER"
        if [[ "$plain" != "$expected" ]]; then
            bad "decrypt on $h: expected '$expected' got '$plain'"; exit 1
        fi
        echo "  $h decrypted ok"
    done
    ok "lockbox round-trip on ${#RECEIVERS[@]} receiver(s)"
fi

# ---------- step 7: gitback (always runs) ----------

step "gitback: init project, push, fan-out, cross-envoy clone"

# Founder inits the project. The project_id is embedded in the printed
# URL line (gitback://<project_id>/<handle>); Founder: gives ed25519:<pub>.
INIT_OUT=$(remote_unlock_and "$FOUNDER" "vigo swarm gitback project init $TAG")
PID=$(echo "$INIT_OUT" | awk '/URL:/{print $2}' | sed -E 's#^gitback://([^/]+)/.*#\1#')
PUDDLE_PUB=$(echo "$INIT_OUT" | awk '/Founder:/{print $2}')
if [[ -z "$PID" ]] || [[ -z "$PUDDLE_PUB" ]]; then
    bad "could not parse project init output"
    echo "$INIT_OUT"; exit 1
fi
URL="gitback://$PID/$TAG"
echo "  project_id=$PID"

# Founder mints invitation; ferry it to each receiver and join.
INVITE_LOCAL=/tmp/$TAG-invite.json
ssh "$FOUNDER" "
    export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null 2>&1
    sleep 1
    vigo swarm gitback project invite $PID --puddle $PUDDLE_PUB --role member --expires 1d 2>/dev/null > /tmp/$TAG-invite.json
"
scp -q "$FOUNDER:/tmp/$TAG-invite.json" "$INVITE_LOCAL"
for h in "${RECEIVERS[@]}"; do
    scp -q "$INVITE_LOCAL" "$h:/tmp/$TAG-invite.json"
    remote_unlock_and "$h" "vigo swarm gitback project join $PID --invitation /tmp/$TAG-invite.json >/dev/null"
done
echo "  ${#RECEIVERS[@]} receiver(s) joined"

# Give the swarm announcer time to gossip per-project presence so the
# pusher's fan-out target list includes every member. Without this
# pause the pusher only sees envoys that announced before the push,
# which on a freshly-joined fleet is just whoever joined first.
sleep 15

# First receiver pushes 3 commits, founder + remaining receivers see them.
PUSHER="${RECEIVERS[0]:-$FOUNDER}"
ssh "$PUSHER" "
    set -e
    export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null 2>&1
    sleep 1
    cd /tmp && rm -rf $TAG-repo && mkdir $TAG-repo && cd $TAG-repo
    git init -q -b master
    git config user.email t@t
    git config user.name t
    for i in 1 2 3; do
        echo \"commit \$i\" > c\$i.txt
        git add c\$i.txt
        git commit -qm \"$TAG c\$i\"
    done
    timeout 10 git push $URL master >/dev/null 2>&1
" || { bad "push from $PUSHER failed"; exit 1; }
HEAD_SHA=$(ssh "$PUSHER" "git -C /tmp/$TAG-repo rev-parse master")
echo "  push from $PUSHER: $HEAD_SHA"

# Verify fan-out: every fleet host's bare repo holds the same master.
deadline=$(( $(date +%s) + 90 ))
while :; do
    ready=0
    for h in "${HOSTS[@]}"; do
        sha=$(ssh "$h" "sudo git -C /var/lib/vigo/swarm/gitback/projects/$PID.git rev-parse refs/heads/master 2>/dev/null" || echo "")
        if [[ "$sha" == "$HEAD_SHA" ]]; then
            ready=$((ready+1))
        fi
    done
    [[ $ready -eq ${#HOSTS[@]} ]] && break
    if [[ $(date +%s) -ge $deadline ]]; then
        bad "fan-out didn't reach all ${#HOSTS[@]} bares within 90s (only $ready)"
        exit 1
    fi
    sleep 3
done
echo "  fan-out: all ${#HOSTS[@]} bares at $HEAD_SHA"

# Cross-envoy clone from a non-pusher.
if [[ ${#RECEIVERS[@]} -ge 2 ]]; then
    CLONER="${RECEIVERS[1]}"
elif [[ "$PUSHER" != "$FOUNDER" ]]; then
    CLONER="$FOUNDER"
else
    CLONER="$PUSHER"   # only one envoy; clone from same host
fi
ssh "$CLONER" "
    set -e
    export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null 2>&1
    sleep 1
    cd /tmp && rm -rf $TAG-clone
    timeout 10 git clone $URL $TAG-clone >/dev/null 2>&1
    cd $TAG-clone
    git cat-file -p refs/remotes/origin/master:c1.txt | grep -q '^commit 1\$'
" || { bad "cross-envoy clone from $CLONER failed"; exit 1; }
echo "  clone from $CLONER: c1.txt content verified"

ok "gitback push + fan-out + cross-envoy clone on ${#HOSTS[@]} envoys"

# ---------- step 8: curator ----------

step "curator: founder publishes artifact, receivers resolve + pull"

# Founder publishes a generic blob. Publishing requires `curator: true`
# on the founder's usercrate + swarm.curator.enabled for this envoy + an
# unlocked puddle (same gating shape as gitback). Read verbs (resolve /
# pull) on receivers are ungated.
CUR_BLOB=/tmp/$TAG-curator.bin
echo "$TAG curator blob $(date -Iseconds)" > "$CUR_BLOB"
CUR_CONTENT=$(cat "$CUR_BLOB")
scp -q "$CUR_BLOB" "$FOUNDER:/tmp/$TAG-curator.bin"

PUSH_OUT=$(ssh "$FOUNDER" "
    set -e
    export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null 2>&1
    sleep 1
    vigo swarm curator push $TAG --file /tmp/$TAG-curator.bin --version 1.0.0 --os linux --arch amd64 --recipients '*' 2>&1
") || { bad "curator push on $FOUNDER failed (needs curator: usercrate flag + swarm.curator.enabled)"; echo "$PUSH_OUT"; exit 1; }
ART_ID=$(echo "$PUSH_OUT" | awk '/artifact_id:/{print $2}')
if [[ -z "$ART_ID" ]]; then
    bad "could not parse artifact_id from curator push output"; echo "$PUSH_OUT"; exit 1
fi
echo "  published artifact_id=$ART_ID"

# Each receiver resolves (catalog propagation + end-to-end signature
# verify, server /resolve), then pulls (blob relay + sha-verified
# materialization) and we assert the materialized bytes round-trip.
# resolve/pull run under sudo: the server-mediated read path needs the
# agent's root-owned mTLS client cert to reach /resolve and /blob — a
# non-root user has "no mTLS material". resolve exits non-zero until the
# artifact propagates, so `|| true` keeps the poll alive under set -e.
for h in "${RECEIVERS[@]}"; do
    deadline=$(( $(date +%s) + 90 ))
    while :; do
        bsha=$(ssh "$h" "sudo vigo swarm curator resolve $ART_ID 1.0.0 linux amd64 2>/dev/null" | awk '/blob_sha:/{print $2}' || true)
        [[ -n "$bsha" ]] && break
        if [[ $(date +%s) -ge $deadline ]]; then
            bad "$h did not resolve the artifact within 90s"; exit 1
        fi
        sleep 3
    done
    PULL_OUT=$(ssh "$h" "export XDG_RUNTIME_DIR=/run/user/\$(id -u); sudo vigo swarm curator pull $ART_ID 1.0.0 linux amd64 2>&1") \
        || { bad "curator pull on $h failed"; echo "$PULL_OUT"; exit 1; }
    MPATH=$(echo "$PULL_OUT" | awk '/Materialized/{print $NF}')
    got=$(ssh "$h" "sudo cat '$MPATH' 2>/dev/null")
    if [[ "$got" != "$CUR_CONTENT" ]]; then
        bad "curator content mismatch on $h (path=$MPATH)"; exit 1
    fi
    echo "  $h resolved + pulled + content verified"
done
ok "curator publish + cross-envoy resolve/pull on ${#RECEIVERS[@]} receiver(s)"

# ---------- step 9: poolq ----------

step "poolq: founder posts to a topic, receivers read the fleet log"

# Founder posts 3 messages. Posting requires `poolq: true` on the
# founder's usercrate + swarm.poolq.enabled for this envoy + an unlocked
# puddle (post is local — no server round-trip). The receiver `tail` reads
# the fleet log over the server REST surface (poolqmesh), so — like curator
# resolve — it needs the agent's root-owned mTLS cert and runs under sudo.
ssh "$FOUNDER" "
    set -e
    export XDG_RUNTIME_DIR=/run/user/\$(id -u)
    echo '$PASSPHRASE' | vigo swarm puddle unlock >/dev/null 2>&1
    sleep 1
    for i in 1 2 3; do
        vigo swarm poolq post $TAG --body \"$TAG poolq msg \$i\" >/dev/null
    done
" || { bad "poolq post on $FOUNDER failed (needs poolq: usercrate flag + swarm.poolq.enabled)"; exit 1; }

# topic_id = the founder's per-topic state dir name, created on first
# post (sha256(founder_puddle_pubkey ‖ topic_name)). Cross-puddle reads
# on receivers must use this hex form, not the bare name.
TOPIC_ID=$(ssh "$FOUNDER" "ls ~/.vigo-poolq/ 2>/dev/null | grep -E '^[0-9a-f]{64}\$' | head -1")
if [[ -z "$TOPIC_ID" ]]; then
    bad "could not determine poolq topic_id from founder state"; exit 1
fi
echo "  posted 3 msgs to topic_id=$TOPIC_ID"

# Each receiver tails the fleet-readable log and must see all 3 messages
# (propagation rides the agent trait → poolqmesh → REST path, so allow a
# check-in cycle).
for h in "${RECEIVERS[@]}"; do
    deadline=$(( $(date +%s) + 90 ))
    while :; do
        n=$(ssh "$h" "sudo vigo swarm poolq tail $TOPIC_ID --limit 10 2>/dev/null" | grep -c 'seq=' || true)
        [[ "$n" -ge 3 ]] && break
        if [[ $(date +%s) -ge $deadline ]]; then
            bad "$h saw $n/3 poolq messages within 90s"; exit 1
        fi
        sleep 3
    done
    echo "  $h tailed 3 message(s)"
done
ok "poolq founder-post + fleet-read on ${#RECEIVERS[@]} receiver(s)"

# ---------- summary ----------

echo
echo "==================== summary ===================="
echo "  fleet:    $FLEET"
echo "  founder:  $FOUNDER"
echo "  tag:      $TAG"
echo "  passed:   $PASS"
echo "  failed:   $FAIL"

if [[ $FAIL -ne 0 ]]; then
    exit 1
fi
exit 0
