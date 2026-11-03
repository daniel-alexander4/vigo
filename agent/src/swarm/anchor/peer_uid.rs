// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! SO_PEERCRED helper — resolve the connecting Unix user's UID for any
//! root-owned helper daemon on a `UnixListener` socket.
//!
//! Every root-side swarm subsystem with a per-user socket needs to know
//! who's connecting (gitback's `/var/run/vigo/gitback.sock`, curator's
//! `/var/run/vigo/curator.sock`, future consumers). The platform `#[cfg]`
//! matrix below is identical across all of them — Linux uses
//! `SO_PEERCRED`, macOS `LOCAL_PEEREID`, the BSDs `getpeereid(3)`,
//! illumos stubbed for now — so the function lives in anchor as a
//! single source of truth instead of being duplicated per consumer.

use std::os::unix::io::AsRawFd;
use tokio::net::UnixStream;

/// Resolve the connecting Unix user's UID via SO_PEERCRED (Linux),
/// LOCAL_PEEREID (macOS), or `getpeereid(3)` (BSDs). Returns `None`
/// when the underlying syscall fails or the platform isn't supported
/// (illumos stubbed; truly-unknown OS also stubbed). Callers treat
/// `None` as "deny" — there is no fallback identity.
pub fn peer_uid(stream: &UnixStream) -> Option<u32> {
    #[cfg(target_os = "linux")]
    {
        use std::mem;
        let fd = stream.as_raw_fd();
        let mut cred: libc::ucred = unsafe { mem::zeroed() };
        let mut len = mem::size_of::<libc::ucred>() as libc::socklen_t;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                &mut cred as *mut _ as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return None;
        }
        Some(cred.uid)
    }
    #[cfg(target_os = "macos")]
    {
        let fd = stream.as_raw_fd();
        // SOL_LOCAL/LOCAL_PEEREID returns (uid, gid) — same shape
        // lockbox's session helper uses.
        let mut cred: [u32; 2] = [0; 2];
        let mut len = std::mem::size_of_val(&cred) as libc::socklen_t;
        const SOL_LOCAL: libc::c_int = 0;
        const LOCAL_PEEREID: libc::c_int = 3;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                SOL_LOCAL,
                LOCAL_PEEREID,
                cred.as_mut_ptr() as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return None;
        }
        return Some(cred[0]);
    }
    #[cfg(any(
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
        target_os = "dragonfly"
    ))]
    {
        // BSD getpeereid(3) — populates uid + gid by FD.
        let fd = stream.as_raw_fd();
        let mut uid: libc::uid_t = 0;
        let mut gid: libc::gid_t = 0;
        let rc = unsafe { libc::getpeereid(fd, &mut uid, &mut gid) };
        if rc != 0 {
            return None;
        }
        return Some(uid);
    }
    #[cfg(target_os = "illumos")]
    {
        // illumos exposes ucred via getpeerucred(3C); fall back to
        // SO_PEERCRED-style getsockopt where supported. Stubbed for now;
        // agent on illumos still loads, peer auth just always denies.
        let _ = stream;
        return None;
    }
    #[cfg(not(any(
        target_os = "linux",
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
        target_os = "dragonfly",
        target_os = "illumos"
    )))]
    {
        let _ = stream;
        None
    }
}
