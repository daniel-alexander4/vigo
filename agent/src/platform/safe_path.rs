// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Symlink-safe filesystem primitives for a root process that reads from or
//! writes into a directory a less-privileged user controls.
//!
//! The agent runs as root and touches files under user homes (scrier's
//! `~/.ssh/authorized_keys`, `~/longdrawer/`, `~/lockbox/`, `~/.vigo-curator/`)
//! and under operator-managed trees a local user may be able to write into
//! (the `directory` executor's `recurse`). Every path-based syscall there
//! (`open`, `chown`, `chmod`, `utimensat`, `canonicalize`) follows a symlink the
//! user planted, which turns a root write into an arbitrary-file overwrite and a
//! root read into an arbitrary-file disclosure.
//!
//! The shape that closes it, used everywhere through this module:
//!
//! 1. Open the directory itself with `O_DIRECTORY | O_NOFOLLOW` and check who
//!    owns it ([`SafeDir::open`] + [`SafeDir::require_owner`]).
//! 2. Do every operation on a single name *relative to that held fd*
//!    (`openat`, `fstatat`, `renameat`, `unlinkat`, `mkdirat`), each with
//!    no-follow semantics. A user swapping a path component after the check
//!    cannot redirect the operation, because the operation never re-resolves
//!    the path.
//! 3. Create files with `O_CREAT | O_EXCL | O_NOFOLLOW` at a restrictive mode,
//!    and set the final mode/owner/mtime on the descriptor (`fchmod`, `fchown`,
//!    `futimens`) before the rename publishes it.
//! 4. Open files for reading with `O_NOFOLLOW | O_NONBLOCK`, require a regular
//!    file (a FIFO would otherwise hang the reader), check its owner (a hard link
//!    to a root file is a regular file owned by root), and cap its size.
//!
//! Ancestors above the opened directory are resolved normally: the anchor is a
//! root-controlled path (the passwd home directory, an operator-configured
//! path). Only the components a user controls are opened no-follow.
//!
//! Unix only (Linux + macOS + the BSDs via libc).

use std::ffi::{CStr, CString, OsStr, OsString};
use std::fs::File;
use std::io::{self, Write};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::os::unix::ffi::{OsStrExt, OsStringExt};
use std::path::{Path, PathBuf};

/// `lstat`-style metadata, taken without following a symlink.
#[derive(Debug, Clone, Copy)]
pub struct Stat {
    pub mode: u32,
    pub uid: u32,
    pub gid: u32,
    pub size: u64,
    pub mtime: i64,
    pub nlink: u64,
}

impl Stat {
    fn from_raw(st: &libc::stat) -> Stat {
        Stat {
            mode: st.st_mode as u32,
            uid: st.st_uid,
            gid: st.st_gid,
            size: st.st_size as u64,
            mtime: st.st_mtime as i64,
            nlink: st.st_nlink as u64,
        }
    }
    fn kind(&self) -> u32 {
        self.mode & (libc::S_IFMT as u32)
    }
    pub fn is_dir(&self) -> bool {
        self.kind() == libc::S_IFDIR as u32
    }
    pub fn is_file(&self) -> bool {
        self.kind() == libc::S_IFREG as u32
    }
    pub fn is_symlink(&self) -> bool {
        self.kind() == libc::S_IFLNK as u32
    }
    /// Permission bits (incl. setuid/setgid/sticky), without the file type.
    pub fn perm(&self) -> u32 {
        self.mode & 0o7777
    }
}

fn cvt(rc: libc::c_int) -> io::Result<libc::c_int> {
    if rc < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(rc)
    }
}

fn invalid(msg: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, msg.to_string())
}

fn path_cstr(p: &Path) -> io::Result<CString> {
    CString::new(p.as_os_str().as_bytes()).map_err(|_| invalid("path contains NUL"))
}

/// A single directory-entry name: non-empty, no `/`, no NUL, not `.`/`..`.
/// Every `*at` call in this module goes through it, so a caller can never
/// smuggle a multi-component path past the no-follow guarantee.
fn name_cstr(name: &OsStr) -> io::Result<CString> {
    let b = name.as_bytes();
    if b.is_empty() || b == b"." || b == b".." || b.contains(&b'/') {
        return Err(invalid("not a single path component"));
    }
    CString::new(b).map_err(|_| invalid("name contains NUL"))
}

fn fstat_raw(fd: RawFd) -> io::Result<Stat> {
    let mut st: libc::stat = unsafe { std::mem::zeroed() };
    // SAFETY: fd is a live descriptor owned by the caller; st is a valid out-pointer.
    cvt(unsafe { libc::fstat(fd, &mut st) })?;
    Ok(Stat::from_raw(&st))
}

/// `fstat` an open file.
pub fn fstat(f: &impl AsRawFd) -> io::Result<Stat> {
    fstat_raw(f.as_raw_fd())
}

/// `fchown` on a descriptor. `None` leaves that id unchanged.
pub fn fchown(f: &impl AsRawFd, uid: Option<u32>, gid: Option<u32>) -> io::Result<()> {
    let u = uid.map(|v| v as libc::uid_t).unwrap_or(libc::uid_t::MAX);
    let g = gid.map(|v| v as libc::gid_t).unwrap_or(libc::gid_t::MAX);
    // SAFETY: plain syscall on a live descriptor.
    cvt(unsafe { libc::fchown(f.as_raw_fd(), u, g) }).map(|_| ())
}

/// `fchmod` on a descriptor.
pub fn fchmod(f: &impl AsRawFd, mode: u32) -> io::Result<()> {
    // SAFETY: plain syscall on a live descriptor.
    cvt(unsafe { libc::fchmod(f.as_raw_fd(), mode as libc::mode_t) }).map(|_| ())
}

/// Set a descriptor's mtime (atime untouched) to `mtime` unix seconds.
pub fn set_mtime_fd(f: &impl AsRawFd, mtime: i64) -> io::Result<()> {
    let times = [
        libc::timespec { tv_sec: 0, tv_nsec: libc::UTIME_OMIT },
        libc::timespec { tv_sec: mtime as _, tv_nsec: 0 },
    ];
    // SAFETY: times is a valid 2-element array; fd is live.
    cvt(unsafe { libc::futimens(f.as_raw_fd(), times.as_ptr()) }).map(|_| ())
}

/// Create a brand-new file at `path` with `O_CREAT | O_EXCL | O_NOFOLLOW` and
/// exactly `mode` (umask is overridden by an `fchmod`). Fails with
/// `AlreadyExists` if anything — including a symlink — is already there, so it
/// can never write through a planted link. For temp files beside a target in
/// an operator-chosen directory (the file / atomic-write executors).
pub fn create_new_file(path: &Path, mode: u32) -> io::Result<File> {
    use std::os::unix::fs::OpenOptionsExt;
    let f = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(mode & 0o777)
        .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)?;
    fchmod(&f, mode)?;
    Ok(f)
}

/// Read `<home>/<subdir>/<name>` as root on behalf of the user `uid`, for a
/// sync directory the user controls (`~/longdrawer`, `~/lockbox`). `home` is
/// the passwd anchor; `<subdir>` must be a real directory owned by `uid` (or
/// root); `<name>` must be a regular file owned by `uid` — not a symlink, not a
/// FIFO, not a hard link to a root-owned file. Larger than `max_len` is
/// `InvalidData`.
pub fn read_user_owned(home: &Path, subdir: &str, name: &str, uid: u32, max_len: u64) -> io::Result<Vec<u8>> {
    SafeDir::open_anchor(home)?
        .subdir(subdir)?
        .require_owner(uid)?
        .read_capped(name, &[uid], max_len)
}

/// A directory held open by descriptor. Every operation is relative to the fd
/// and takes a single entry name; nothing re-resolves the directory's path.
#[derive(Debug)]
pub struct SafeDir {
    fd: OwnedFd,
    path: PathBuf,
}

impl SafeDir {
    fn open_flags(path: &Path, flags: libc::c_int) -> io::Result<SafeDir> {
        let c = path_cstr(path)?;
        // SAFETY: c is a valid NUL-terminated path.
        let fd = cvt(unsafe { libc::open(c.as_ptr(), flags) })?;
        // SAFETY: fd was just returned by open and is owned by nobody else.
        let fd = unsafe { OwnedFd::from_raw_fd(fd) };
        Ok(SafeDir { fd, path: path.to_path_buf() })
    }

    /// Open `path` as a directory, refusing (`ELOOP`/`ENOTDIR`) if its final
    /// component is a symlink. Ancestors resolve normally — `path`'s parent
    /// must be a root-controlled location.
    pub fn open(path: &Path) -> io::Result<SafeDir> {
        Self::open_flags(
            path,
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
        )
    }

    /// Open a root-controlled anchor directory (e.g. the passwd home), following
    /// symlinks. Only for paths a non-root user cannot redirect.
    pub fn open_anchor(path: &Path) -> io::Result<SafeDir> {
        Self::open_flags(path, libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC)
    }

    /// Open `anchor` (root-controlled, followed) and then each component of
    /// the relative path `rel` no-follow, requiring every one of them to be
    /// owned by `uid` or root. For a directory several levels inside a user's
    /// home (`~/.lockbox/staging`), where any intermediate component could be
    /// a planted symlink.
    pub fn open_beneath(anchor: &Path, rel: &Path, uid: u32) -> io::Result<SafeDir> {
        use std::path::Component;
        let mut dir = Self::open_anchor(anchor)?;
        for c in rel.components() {
            match c {
                Component::Normal(name) => dir = dir.subdir(name)?.require_owner(uid)?,
                Component::CurDir => {}
                _ => return Err(invalid("path escapes its anchor")),
            }
        }
        Ok(dir)
    }

    /// [`SafeDir::open`] plus [`SafeDir::require_owner`].
    pub fn open_owned(path: &Path, uid: u32) -> io::Result<SafeDir> {
        Self::open(path)?.require_owner(uid)
    }

    /// The path this directory was opened at (for messages only — never
    /// re-resolve it for an operation).
    pub fn path(&self) -> &Path {
        &self.path
    }

    pub fn stat_self(&self) -> io::Result<Stat> {
        fstat_raw(self.fd.as_raw_fd())
    }

    /// Require the directory to be owned by `uid` or by root. A root-owned
    /// directory is not attacker-controlled; anything else is refused with
    /// `PermissionDenied`.
    pub fn require_owner(self, uid: u32) -> io::Result<SafeDir> {
        let st = self.stat_self()?;
        if st.uid != uid && st.uid != 0 {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                format!(
                    "{} is owned by uid {}, expected {} (or root)",
                    self.path.display(),
                    st.uid,
                    uid
                ),
            ));
        }
        Ok(self)
    }

    /// `fstatat(name, AT_SYMLINK_NOFOLLOW)`.
    pub fn stat(&self, name: impl AsRef<OsStr>) -> io::Result<Stat> {
        let c = name_cstr(name.as_ref())?;
        let mut st: libc::stat = unsafe { std::mem::zeroed() };
        // SAFETY: valid dirfd, NUL-terminated name, valid out-pointer.
        cvt(unsafe {
            libc::fstatat(self.fd.as_raw_fd(), c.as_ptr(), &mut st, libc::AT_SYMLINK_NOFOLLOW)
        })?;
        Ok(Stat::from_raw(&st))
    }

    /// Open the child directory `name`, refusing a symlink.
    pub fn subdir(&self, name: impl AsRef<OsStr>) -> io::Result<SafeDir> {
        let name = name.as_ref();
        let c = name_cstr(name)?;
        // SAFETY: valid dirfd + NUL-terminated single-component name.
        let fd = cvt(unsafe {
            libc::openat(
                self.fd.as_raw_fd(),
                c.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            )
        })?;
        // SAFETY: fd freshly returned by openat.
        let fd = unsafe { OwnedFd::from_raw_fd(fd) };
        Ok(SafeDir { fd, path: self.path.join(name) })
    }

    /// `mkdirat(name, mode)`. Returns `Ok(true)` if created, `Ok(false)` if the
    /// name already exists (whatever it is — open it with [`SafeDir::subdir`]
    /// to find out; that refuses a symlink).
    pub fn mkdir(&self, name: impl AsRef<OsStr>, mode: u32) -> io::Result<bool> {
        let c = name_cstr(name.as_ref())?;
        // SAFETY: valid dirfd + NUL-terminated single-component name.
        let rc = unsafe { libc::mkdirat(self.fd.as_raw_fd(), c.as_ptr(), mode as libc::mode_t) };
        if rc == 0 {
            return Ok(true);
        }
        let e = io::Error::last_os_error();
        if e.kind() == io::ErrorKind::AlreadyExists {
            Ok(false)
        } else {
            Err(e)
        }
    }

    /// Create `name` fresh (`O_CREAT|O_EXCL|O_NOFOLLOW`) with exactly `mode`.
    /// Fails `AlreadyExists` if anything is there.
    pub fn create_new(&self, name: impl AsRef<OsStr>, mode: u32) -> io::Result<File> {
        let c = name_cstr(name.as_ref())?;
        // SAFETY: valid dirfd + NUL-terminated single-component name.
        let fd = cvt(unsafe {
            libc::openat(
                self.fd.as_raw_fd(),
                c.as_ptr(),
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                (mode & 0o777) as libc::c_uint,
            )
        })?;
        // SAFETY: fd freshly returned by openat.
        let f = unsafe { File::from_raw_fd(fd) };
        fchmod(&f, mode)?;
        Ok(f)
    }

    /// Like [`SafeDir::create_new`], but first unlinks a stale entry left at
    /// `name` by a crashed earlier attempt. `unlinkat` removes a symlink itself,
    /// never its target, and a link re-planted in the gap makes the exclusive
    /// create fail rather than follow it.
    pub fn create_replacing(&self, name: impl AsRef<OsStr>, mode: u32) -> io::Result<File> {
        let name = name.as_ref();
        match self.unlink(name) {
            Ok(()) => {}
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => return Err(e),
        }
        self.create_new(name, mode)
    }

    /// Open `name` for reading without following a symlink and without
    /// blocking on a FIFO. Requires a regular file, an owner in
    /// `allowed_owners` (empty = any), and a size of at most `max_len`
    /// (`FileTooLarge`-style `InvalidData` otherwise). Returns the file
    /// (switched back to blocking) and its stat.
    pub fn open_read(
        &self,
        name: impl AsRef<OsStr>,
        allowed_owners: &[u32],
        max_len: u64,
    ) -> io::Result<(File, Stat)> {
        let c = name_cstr(name.as_ref())?;
        // SAFETY: valid dirfd + NUL-terminated single-component name.
        let fd = cvt(unsafe {
            libc::openat(
                self.fd.as_raw_fd(),
                c.as_ptr(),
                libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_CLOEXEC,
            )
        })?;
        // SAFETY: fd freshly returned by openat.
        let f = unsafe { File::from_raw_fd(fd) };
        let st = fstat(&f)?;
        if !st.is_file() {
            return Err(io::Error::new(io::ErrorKind::InvalidInput, "not a regular file"));
        }
        if !allowed_owners.is_empty() && !allowed_owners.contains(&st.uid) {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                format!("file owned by uid {}, not an allowed owner", st.uid),
            ));
        }
        if st.size > max_len {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("file is {} bytes, cap {}", st.size, max_len),
            ));
        }
        // Back to blocking I/O for the read itself.
        // SAFETY: fcntl on a live descriptor.
        let fl = cvt(unsafe { libc::fcntl(f.as_raw_fd(), libc::F_GETFL) })?;
        cvt(unsafe { libc::fcntl(f.as_raw_fd(), libc::F_SETFL, fl & !libc::O_NONBLOCK) })?;
        Ok((f, st))
    }

    /// Read a whole file through [`SafeDir::open_read`], never more than
    /// `max_len` bytes even if it grows after the stat.
    pub fn read_capped(
        &self,
        name: impl AsRef<OsStr>,
        allowed_owners: &[u32],
        max_len: u64,
    ) -> io::Result<Vec<u8>> {
        use std::io::Read;
        let (f, st) = self.open_read(name, allowed_owners, max_len)?;
        let mut buf = Vec::with_capacity(st.size as usize);
        f.take(max_len).read_to_end(&mut buf)?;
        Ok(buf)
    }

    /// `renameat(self/from → to_dir/to)`. Neither name is followed.
    pub fn rename_to(
        &self,
        from: impl AsRef<OsStr>,
        to_dir: &SafeDir,
        to: impl AsRef<OsStr>,
    ) -> io::Result<()> {
        let a = name_cstr(from.as_ref())?;
        let b = name_cstr(to.as_ref())?;
        // SAFETY: valid dirfds + NUL-terminated single-component names.
        cvt(unsafe {
            libc::renameat(self.fd.as_raw_fd(), a.as_ptr(), to_dir.fd.as_raw_fd(), b.as_ptr())
        })
        .map(|_| ())
    }

    /// `renameat` within this directory.
    pub fn rename(&self, from: impl AsRef<OsStr>, to: impl AsRef<OsStr>) -> io::Result<()> {
        self.rename_to(from, self, to)
    }

    /// `unlinkat(name)` — removes the entry itself (a symlink, not its target).
    pub fn unlink(&self, name: impl AsRef<OsStr>) -> io::Result<()> {
        let c = name_cstr(name.as_ref())?;
        // SAFETY: valid dirfd + NUL-terminated single-component name.
        cvt(unsafe { libc::unlinkat(self.fd.as_raw_fd(), c.as_ptr(), 0) }).map(|_| ())
    }

    /// `fchownat(name, AT_SYMLINK_NOFOLLOW)` — changes a symlink itself, never
    /// its target.
    pub fn lchown(&self, name: impl AsRef<OsStr>, uid: Option<u32>, gid: Option<u32>) -> io::Result<()> {
        let c = name_cstr(name.as_ref())?;
        let u = uid.map(|v| v as libc::uid_t).unwrap_or(libc::uid_t::MAX);
        let g = gid.map(|v| v as libc::gid_t).unwrap_or(libc::gid_t::MAX);
        // SAFETY: valid dirfd + NUL-terminated single-component name.
        cvt(unsafe {
            libc::fchownat(self.fd.as_raw_fd(), c.as_ptr(), u, g, libc::AT_SYMLINK_NOFOLLOW)
        })
        .map(|_| ())
    }

    /// `fchown` the directory itself.
    pub fn chown_self(&self, uid: Option<u32>, gid: Option<u32>) -> io::Result<()> {
        fchown(&self.fd, uid, gid)
    }

    /// `fchmod` the directory itself.
    pub fn chmod_self(&self, mode: u32) -> io::Result<()> {
        fchmod(&self.fd, mode)
    }

    /// List entry names (without `.`/`..`).
    pub fn entries(&self) -> io::Result<Vec<OsString>> {
        // fdopendir takes ownership of the fd it is given, so hand it a dup.
        // SAFETY: dup on a live descriptor.
        let dupfd = cvt(unsafe { libc::fcntl(self.fd.as_raw_fd(), libc::F_DUPFD_CLOEXEC, 0) })?;
        // SAFETY: dupfd is ours; on success the DIR owns it.
        let dirp = unsafe { libc::fdopendir(dupfd) };
        if dirp.is_null() {
            let e = io::Error::last_os_error();
            unsafe { libc::close(dupfd) };
            return Err(e);
        }
        // A dup shares the file offset; start from the beginning regardless.
        unsafe { libc::rewinddir(dirp) };
        let mut out = Vec::new();
        loop {
            // SAFETY: dirp is a live DIR*; the returned dirent is valid until
            // the next readdir/closedir and we copy the name out immediately.
            let ent = unsafe { libc::readdir(dirp) };
            if ent.is_null() {
                break;
            }
            let name = unsafe { CStr::from_ptr((*ent).d_name.as_ptr()) }.to_bytes();
            if name == b"." || name == b".." {
                continue;
            }
            out.push(OsString::from_vec(name.to_vec()));
        }
        unsafe { libc::closedir(dirp) };
        Ok(out)
    }

    /// Atomically publish `data` at `name`: write a fresh `O_EXCL|O_NOFOLLOW`
    /// temp (`tmp_name`) at mode 0600, set `mode` / `owner` / `mtime` on its
    /// descriptor, fsync, then `renameat` over `name`. On any error the temp
    /// is unlinked and `name` is untouched.
    pub fn write_atomic(
        &self,
        name: impl AsRef<OsStr>,
        tmp_name: impl AsRef<OsStr>,
        data: &[u8],
        mode: u32,
        owner: Option<(u32, u32)>,
        mtime: Option<i64>,
    ) -> io::Result<()> {
        let tmp_name = tmp_name.as_ref();
        let res = (|| -> io::Result<()> {
            let mut f = self.create_replacing(tmp_name, 0o600)?;
            f.write_all(data)?;
            self.finish_file(&f, mode, owner, mtime)?;
            drop(f);
            self.rename(tmp_name, name.as_ref())
        })();
        if res.is_err() {
            let _ = self.unlink(tmp_name);
        }
        res
    }

    /// Set owner, mode and mtime on a freshly-written file's descriptor and
    /// fsync it — the step before the rename that publishes it.
    pub fn finish_file(
        &self,
        f: &File,
        mode: u32,
        owner: Option<(u32, u32)>,
        mtime: Option<i64>,
    ) -> io::Result<()> {
        if let Some((uid, gid)) = owner {
            fchown(f, Some(uid), Some(gid))?;
        }
        // chown clears setuid/setgid; set the mode after it.
        fchmod(f, mode)?;
        if let Some(t) = mtime {
            set_mtime_fd(f, t)?;
        }
        f.sync_all()
    }
}

impl AsRawFd for SafeDir {
    fn as_raw_fd(&self) -> RawFd {
        self.fd.as_raw_fd()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::symlink;

    fn uid() -> u32 {
        unsafe { libc::getuid() }
    }

    #[test]
    fn open_refuses_symlinked_dir() {
        let t = tempfile::tempdir().unwrap();
        let real = t.path().join("real");
        std::fs::create_dir(&real).unwrap();
        let link = t.path().join("link");
        symlink(&real, &link).unwrap();
        assert!(SafeDir::open(&real).is_ok());
        assert!(SafeDir::open(&link).is_err(), "a symlinked dir must not open");
        let root = SafeDir::open(t.path()).unwrap();
        assert!(root.subdir("link").is_err());
        assert!(root.subdir("real").is_ok());
    }

    #[test]
    fn require_owner_refuses_foreign_uid() {
        let t = tempfile::tempdir().unwrap();
        let d = SafeDir::open(t.path()).unwrap();
        let me = uid();
        assert!(SafeDir::open_owned(t.path(), me).is_ok());
        if me != 0 {
            // Owned by `me`, neither root nor `me + 1`.
            assert!(d.require_owner(me + 1).is_err());
        }
    }

    #[test]
    fn create_new_never_writes_through_a_symlink() {
        let t = tempfile::tempdir().unwrap();
        let outside = t.path().join("outside");
        std::fs::write(&outside, b"precious").unwrap();
        let d = t.path().join("d");
        std::fs::create_dir(&d).unwrap();
        symlink(&outside, d.join("tmp")).unwrap();
        let dir = SafeDir::open(&d).unwrap();
        assert!(dir.create_new("tmp", 0o600).is_err());
        // create_replacing removes the link itself and creates a real file.
        let mut f = dir.create_replacing("tmp", 0o600).unwrap();
        f.write_all(b"new").unwrap();
        assert_eq!(std::fs::read(&outside).unwrap(), b"precious");
        assert!(!std::fs::symlink_metadata(d.join("tmp")).unwrap().file_type().is_symlink());
        assert_eq!(dir.stat("tmp").unwrap().perm(), 0o600);
    }

    #[test]
    fn open_read_refuses_symlink_fifo_and_oversize() {
        let t = tempfile::tempdir().unwrap();
        let outside = t.path().join("outside");
        std::fs::write(&outside, b"secret").unwrap();
        let d = t.path().join("d");
        std::fs::create_dir(&d).unwrap();
        symlink(&outside, d.join("link")).unwrap();
        std::fs::write(d.join("big"), vec![0u8; 100]).unwrap();
        let fifo = CString::new(d.join("fifo").as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o600) }, 0);
        let dir = SafeDir::open(&d).unwrap();
        assert!(dir.open_read("link", &[], 1 << 20).is_err());
        assert!(dir.open_read("fifo", &[], 1 << 20).is_err(), "fifo must not hang or open");
        assert!(dir.open_read("big", &[], 10).is_err());
        assert_eq!(dir.read_capped("big", &[uid()], 100).unwrap().len(), 100);
        if uid() != 0 {
            assert!(dir.open_read("big", &[uid() + 1], 100).is_err());
        }
    }

    #[test]
    fn names_must_be_single_components() {
        let t = tempfile::tempdir().unwrap();
        let dir = SafeDir::open(t.path()).unwrap();
        for bad in ["", ".", "..", "a/b", "../x"] {
            assert!(dir.create_new(bad, 0o600).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn write_atomic_sets_mode_and_mtime_and_replaces() {
        let t = tempfile::tempdir().unwrap();
        let dir = SafeDir::open(t.path()).unwrap();
        std::fs::write(t.path().join("f"), b"old").unwrap();
        dir.write_atomic("f", ".f.tmp", b"new", 0o640, None, Some(1000)).unwrap();
        assert_eq!(std::fs::read(t.path().join("f")).unwrap(), b"new");
        let st = dir.stat("f").unwrap();
        assert_eq!(st.perm(), 0o640);
        assert_eq!(st.mtime, 1000);
        assert!(dir.stat(".f.tmp").is_err());
    }

    #[test]
    fn entries_lists_names() {
        let t = tempfile::tempdir().unwrap();
        std::fs::write(t.path().join("a"), b"").unwrap();
        std::fs::create_dir(t.path().join("b")).unwrap();
        let dir = SafeDir::open(t.path()).unwrap();
        let mut e = dir.entries().unwrap();
        e.sort();
        assert_eq!(e, vec![OsString::from("a"), OsString::from("b")]);
        // Twice — the dup'd offset must not leave the second listing empty.
        assert_eq!(dir.entries().unwrap().len(), 2);
    }
}
