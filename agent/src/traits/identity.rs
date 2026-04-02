// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

use anyhow::Result;
use serde_json::{json, Value};

pub fn collect() -> Result<Value> {
    let uid = nix::unistd::getuid().as_raw();
    let gid = nix::unistd::getgid().as_raw();
    let euid = nix::unistd::geteuid().as_raw();

    let user = nix::unistd::User::from_uid(nix::unistd::Uid::from_raw(uid))
        .ok()
        .flatten()
        .map(|u| u.name)
        .unwrap_or_default();

    let group = nix::unistd::Group::from_gid(nix::unistd::Gid::from_raw(gid))
        .ok()
        .flatten()
        .map(|g| g.name)
        .unwrap_or_default();

    Ok(json!({
        "uid": uid,
        "gid": gid,
        "user": user,
        "group": group,
        "privileged": euid == 0,
        "agent_version": env!("CARGO_PKG_VERSION"),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_collect_returns_expected_keys() {
        let val = collect().unwrap();
        let obj = val.as_object().unwrap();
        assert!(obj.contains_key("uid"));
        assert!(obj.contains_key("gid"));
        assert!(obj.contains_key("user"));
        assert!(obj.contains_key("group"));
        assert!(obj.contains_key("privileged"));
        assert!(obj.contains_key("agent_version"));
        assert!(obj["uid"].is_number());
        assert!(obj["privileged"].is_boolean());
        assert!(obj["agent_version"].is_string());
    }
}
