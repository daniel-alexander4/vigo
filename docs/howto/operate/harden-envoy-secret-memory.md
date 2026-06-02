
# Harden an envoy against secret-memory disclosure

When an envoy applies a configcrate that references a `secret:`, the agent fetches
the plaintext from the server and holds it **in process memory** for the
lifetime of that secret epoch — in a resolved-secrets cache, and in the
short-lived copies the substitute → render → apply pipeline makes (the
rendered file `content:`, exec environment values, command strings). This is
unavoidable: the agent has to have the cleartext to write the file or run the
command. The question this page answers is how to keep that cleartext from
reaching **disk** or **another process**.

Most of that boundary is already enforced by the agent itself. The one piece
that is the operator's to close is **swap**.

## What the agent already does

On startup, before it resolves any secret, the agent hardens its own process
(`harden_process_memory`, best-effort, logged on failure):

| Mitigation | Platform | What it stops |
|---|---|---|
| `RLIMIT_CORE = 0` | all Unix | A crash produces no core dump, so secret bytes can't be captured in a core file on disk. |
| `PR_SET_DUMPABLE = 0` | Linux | Marks the process non-dumpable — blocks `ptrace` attach and `/proc/<pid>/mem` reads by another same-uid process. |

You don't configure these; they're always on. To confirm the agent is running
non-dumpable on Linux:

```sh
# 0 = non-dumpable (hardened). Run as the user the agent runs as (root).
cat /proc/$(pgrep -x vigo)/status | grep -i dumpable
# Dumpable:  0
```

## The residual: swap

What the process **cannot** cleanly close on its own is swap. Under memory
pressure the kernel can page any of the agent's memory — including the pages
holding resolved secrets — out to the swap device, where the cleartext then
sits on disk outside the agent's control. The agent deliberately does **not**
call `mlockall` to pin its memory into RAM: that would defeat the
bounded-footprint, stay-invisible doctrine the agent is built around (and can
fail outright under a restrictive `RLIMIT_MEMLOCK`). Closing the swap path is
therefore a host-configuration decision, and it belongs to the operator.

On any host where envoys resolve secrets, do one of the following.

### Option A — no swap (simplest)

If the host is sized so it doesn't need swap, disable it. Secret pages can
never reach a device that isn't there.

```sh
sudo swapoff -a
# Then remove or comment the swap entries in /etc/fstab so it stays off
# across reboots, e.g.:
#   sudo sed -i.bak '/\sswap\s/s/^/#/' /etc/fstab
```

### Option B — encrypted swap (keep swap, close the exposure)

If the host needs swap, encrypt it so anything paged out is unreadable at
rest. Two common approaches:

- **`zram`** — a compressed swap device in RAM (never touches disk at all).
  Good for hosts that occasionally spike but have RAM headroom. Most distros
  ship a `zram-generator` / `systemd-zram-setup@.service`.
- **dm-crypt swap with an ephemeral key** — a `cryptsetup` swap target keyed
  from `/dev/urandom` at boot, so the swap contents are unrecoverable after a
  power cycle. On Debian/Ubuntu this is a `/etc/crypttab` entry such as:

  ```
  # /etc/crypttab
  cryptswap  /dev/disk/by-uuid/<SWAP-UUID>  /dev/urandom  swap,cipher=aes-xts-plain64,size=256
  ```

  with the matching `/dev/mapper/cryptswap` line in `/etc/fstab`.

Either option closes the residual. Pick based on whether the host needs real
swap capacity (dm-crypt) or just burst headroom (zram / none).

## Scope

This page is about hardening the **host the agent runs on** against disclosure
of the agent's own secret memory. It is distinct from
[`examples/security-hardening.md`](../../examples/security-hardening.md), which is
about using Vigo *modules* to harden the managed nodes themselves (firewall,
SSH, sudo policy).

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../../legal/license.md).*
