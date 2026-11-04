// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use serde_json::{json, Value};
use std::collections::BTreeSet;

/// Well-known OS accounts present on virtually all Linux distributions.
/// Anything UID < 1000 not in this set is classified as thirdparty.
const OS_ACCOUNTS: &[&str] = &[
    "root", "bin", "daemon", "sys", "sync", "games", "man", "lp", "mail",
    "news", "uucp", "proxy", "www-data", "backup", "list", "irc", "nobody",
    "_apt", "messagebus", "sshd", "polkitd", "rtkit",
    // systemd service accounts (name varies but prefix is stable)
];

/// Shells that indicate a non-interactive (service) account.
const NOLOGIN_SHELLS: &[&str] = &[
    "/usr/sbin/nologin",
    "/sbin/nologin",
    "/bin/false",
    "/usr/bin/false",
];

pub fn collect() -> Result<Value> {
    let logged_in = logged_in_users();
    let passwd = read_passwd().unwrap_or_default();
    let (human, os_users, thirdparty) = classify_accounts(&passwd);

    Ok(json!({
        "logged_in": logged_in.into_iter().collect::<Vec<_>>(),
        "human": human,
        "os": os_users,
        "thirdparty": thirdparty,
    }))
}

/// A parsed /etc/passwd entry.
struct PasswdEntry {
    name: String,
    uid: u32,
    shell: String,
}

/// Reads and parses /etc/passwd.
fn read_passwd() -> Result<Vec<PasswdEntry>> {
    let content = std::fs::read_to_string("/etc/passwd")?;
    Ok(parse_passwd(&content))
}

/// Parses /etc/passwd content into entries.
fn parse_passwd(content: &str) -> Vec<PasswdEntry> {
    content
        .lines()
        .filter_map(|line| {
            let fields: Vec<&str> = line.split(':').collect();
            if fields.len() < 7 {
                return None;
            }
            let uid = fields[2].parse::<u32>().ok()?;
            Some(PasswdEntry {
                name: fields[0].to_string(),
                uid,
                shell: fields[6].to_string(),
            })
        })
        .collect()
}

/// Classifies passwd entries into human, os, and thirdparty lists.
fn classify_accounts(entries: &[PasswdEntry]) -> (Vec<String>, Vec<String>, Vec<String>) {
    let os_set: BTreeSet<&str> = OS_ACCOUNTS.iter().copied().collect();
    let mut human = Vec::new();
    let mut os_users = Vec::new();
    let mut thirdparty = Vec::new();

    for entry in entries {
        if entry.uid >= 1000 && entry.uid != 65534 && !is_nologin_shell(&entry.shell) {
            human.push(entry.name.clone());
        } else if entry.uid < 1000 || entry.uid == 65534 {
            if os_set.contains(entry.name.as_str())
                || entry.name.starts_with("systemd-")
            {
                os_users.push(entry.name.clone());
            } else {
                thirdparty.push(entry.name.clone());
            }
        }
    }

    (human, os_users, thirdparty)
}

/// Returns true if the shell is a known non-interactive shell.
fn is_nologin_shell(shell: &str) -> bool {
    NOLOGIN_SHELLS.contains(&shell)
}

/// Returns the set of currently logged-in usernames via `who`.
fn logged_in_users() -> BTreeSet<String> {
    let output = match crate::traits::run_cmd("who", &[]) {
        Ok(o) if o.status.success() => String::from_utf8_lossy(&o.stdout).to_string(),
        _ => return BTreeSet::new(),
    };

    output
        .lines()
        .filter_map(|line| {
            let user = line.split_whitespace().next()?;
            if user.is_empty() {
                None
            } else {
                Some(user.to_string())
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("logged_in"), "missing logged_in key");
        assert!(obj.contains_key("human"), "missing human key");
        assert!(obj.contains_key("os"), "missing os key");
        assert!(obj.contains_key("thirdparty"), "missing thirdparty key");
        assert!(obj["logged_in"].is_array());
        assert!(obj["human"].is_array());
        assert!(obj["os"].is_array());
        assert!(obj["thirdparty"].is_array());
    }

    #[test]
    fn test_parse_passwd_basic() {
        let content = "\
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
dan:x:1000:1000:Dan:/home/dan:/bin/bash
alice:x:1001:1001:Alice:/home/alice:/bin/zsh
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
";
        let entries = parse_passwd(content);
        assert_eq!(entries.len(), 5);
        assert_eq!(entries[0].name, "root");
        assert_eq!(entries[0].uid, 0);
        assert_eq!(entries[2].name, "dan");
        assert_eq!(entries[2].uid, 1000);
    }

    #[test]
    fn test_classify_human_accounts() {
        let content = "\
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
dan:x:1000:1000:Dan:/home/dan:/bin/bash
alice:x:1001:1001:Alice:/home/alice:/bin/zsh
bob:x:1002:1002:Bob:/home/bob:/bin/bash
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
";
        let entries = parse_passwd(content);
        let (human, _, _) = classify_accounts(&entries);
        assert_eq!(human, vec!["dan", "alice", "bob"]);
    }

    #[test]
    fn test_classify_nologin_high_uid_excluded() {
        let content = "\
nfsnobody:x:65534:65534:Anonymous NFS User:/var/lib/nfs:/usr/sbin/nologin
svcacct:x:1100:1100:Service:/opt/svc:/usr/sbin/nologin
dan:x:1000:1000:Dan:/home/dan:/bin/bash
";
        let entries = parse_passwd(content);
        let (human, _, _) = classify_accounts(&entries);
        assert_eq!(human, vec!["dan"]);
    }

    #[test]
    fn test_classify_os_vs_thirdparty() {
        let content = "\
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
sys:x:3:3:sys:/dev:/usr/sbin/nologin
systemd-network:x:100:102:systemd Network:/run/systemd:/usr/sbin/nologin
postgres:x:108:115:PostgreSQL:/var/lib/postgresql:/bin/bash
nginx:x:109:116:nginx:/nonexistent:/usr/sbin/nologin
redis:x:110:117:Redis:/var/lib/redis:/usr/sbin/nologin
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
";
        let entries = parse_passwd(content);
        let (_, os_users, thirdparty) = classify_accounts(&entries);
        assert!(os_users.contains(&"root".to_string()));
        assert!(os_users.contains(&"daemon".to_string()));
        assert!(os_users.contains(&"sys".to_string()));
        assert!(os_users.contains(&"systemd-network".to_string()));
        assert!(os_users.contains(&"nobody".to_string()));
        assert!(thirdparty.contains(&"postgres".to_string()));
        assert!(thirdparty.contains(&"nginx".to_string()));
        assert!(thirdparty.contains(&"redis".to_string()));
    }

    #[test]
    fn test_classify_empty_passwd() {
        let entries = parse_passwd("");
        let (human, os_users, thirdparty) = classify_accounts(&entries);
        assert!(human.is_empty());
        assert!(os_users.is_empty());
        assert!(thirdparty.is_empty());
    }

    #[test]
    fn test_is_nologin_shell() {
        assert!(is_nologin_shell("/usr/sbin/nologin"));
        assert!(is_nologin_shell("/sbin/nologin"));
        assert!(is_nologin_shell("/bin/false"));
        assert!(!is_nologin_shell("/bin/bash"));
        assert!(!is_nologin_shell("/bin/zsh"));
    }
}
