// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Strict parser and validator for the mandatory `.vigogit.yaml` sidecar.
//!
//! Every field in the sidecar is mandatory. Any missing field, unknown
//! field, duplicate key, out-of-range value, or malformed YAML causes
//! the sidecar to be rejected and the directory to be skipped.
//! `max_mirrors` is clamped to the [`MIN_MIRRORS`][super::MIN_MIRRORS],
//! [`MAX_MIRRORS`][super::MAX_MIRRORS] range with a warning log if the
//! operator set a value outside.
//!
//! The file must be ≤ [`MAX_SIDECAR_BYTES`] bytes. Larger files are
//! rejected without parsing as a defense against YAML bombs.

use serde::Deserialize;
use std::fs;
use std::path::Path;
use thiserror::Error;
use tracing::warn;

use super::{MAX_MIRRORS, MIN_MIRRORS};

/// Maximum allowed size of a `.vigogit.yaml` file, in bytes.
pub const MAX_SIDECAR_BYTES: u64 = 8 * 1024;

/// Maximum length of the `description` field, in characters.
pub const MAX_DESCRIPTION_LEN: usize = 256;

/// Maximum number of entries allowed in the `tags` list.
pub const MAX_TAGS: usize = 16;

/// Maximum length of a single tag string, in characters.
pub const MAX_TAG_LEN: usize = 32;

/// Validated sidecar contents. All fields are mandatory in the source
/// file; this struct reflects the post-validation, post-clamp values
/// that the rest of vigogit consumes.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SidecarConfig {
    pub owner: String,
    pub max_mirrors: usize,
    pub tags: Vec<String>,
    pub paused: bool,
    pub description: String,
}

/// Raw deserialization target. `deny_unknown_fields` + the fact that
/// every field is non-Optional together enforce the "all mandatory"
/// rule at serde level.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct RawSidecar {
    owner: String,
    max_mirrors: i64,
    tags: Vec<String>,
    paused: bool,
    description: String,
}

#[derive(Debug, Error)]
pub enum SidecarError {
    #[error("sidecar file not found")]
    NotFound,
    #[error("sidecar exceeds {MAX_SIDECAR_BYTES} byte cap ({0} bytes)")]
    TooLarge(u64),
    #[error("sidecar I/O error: {0}")]
    Io(#[source] std::io::Error),
    #[error("sidecar YAML parse error: {0}")]
    Parse(#[source] serde_yaml::Error),
    #[error("sidecar owner field '{0}' is not a valid hostname")]
    InvalidOwner(String),
    #[error("sidecar max_mirrors {0} is negative")]
    NegativeMaxMirrors(i64),
    #[error(
        "sidecar description exceeds {MAX_DESCRIPTION_LEN} character limit ({0} chars)"
    )]
    DescriptionTooLong(usize),
    #[error("sidecar has {0} tags (maximum {MAX_TAGS})")]
    TooManyTags(usize),
    #[error("sidecar tag '{0}' is empty or exceeds {MAX_TAG_LEN} character limit")]
    InvalidTag(String),
}

/// Load and validate a sidecar from a filesystem path.
pub fn load(path: &Path) -> Result<SidecarConfig, SidecarError> {
    let meta = match fs::metadata(path) {
        Ok(m) => m,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return Err(SidecarError::NotFound);
        }
        Err(e) => return Err(SidecarError::Io(e)),
    };

    if meta.len() > MAX_SIDECAR_BYTES {
        return Err(SidecarError::TooLarge(meta.len()));
    }

    let contents = fs::read_to_string(path).map_err(SidecarError::Io)?;
    parse_str(&contents)
}

/// Parse and validate sidecar YAML from a string. Exposed for unit
/// tests and for cross-language validator parity checks.
pub fn parse_str(input: &str) -> Result<SidecarConfig, SidecarError> {
    if input.len() as u64 > MAX_SIDECAR_BYTES {
        return Err(SidecarError::TooLarge(input.len() as u64));
    }
    let raw: RawSidecar = serde_yaml::from_str(input).map_err(SidecarError::Parse)?;
    validate(raw)
}

fn validate(raw: RawSidecar) -> Result<SidecarConfig, SidecarError> {
    // owner — hostname pattern
    let owner = raw.owner.trim().to_lowercase();
    if !is_valid_hostname(&owner) {
        return Err(SidecarError::InvalidOwner(raw.owner));
    }

    // max_mirrors — non-negative, then clamp to [MIN_MIRRORS, MAX_MIRRORS]
    if raw.max_mirrors < 0 {
        return Err(SidecarError::NegativeMaxMirrors(raw.max_mirrors));
    }
    let requested = raw.max_mirrors as usize;
    let effective = requested.clamp(MIN_MIRRORS, MAX_MIRRORS);
    if effective != requested {
        warn!(
            requested = requested,
            effective = effective,
            min = MIN_MIRRORS,
            max = MAX_MIRRORS,
            "vigogit: max_mirrors clamped to valid range"
        );
    }

    // tags
    if raw.tags.len() > MAX_TAGS {
        return Err(SidecarError::TooManyTags(raw.tags.len()));
    }
    for tag in &raw.tags {
        if tag.is_empty() || tag.chars().count() > MAX_TAG_LEN {
            return Err(SidecarError::InvalidTag(tag.clone()));
        }
    }

    // description
    if raw.description.chars().count() > MAX_DESCRIPTION_LEN {
        return Err(SidecarError::DescriptionTooLong(raw.description.chars().count()));
    }

    Ok(SidecarConfig {
        owner,
        max_mirrors: effective,
        tags: raw.tags,
        paused: raw.paused,
        description: raw.description,
    })
}

/// Validate that `s` matches the hostname pattern
/// `[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)*`.
/// Callers must already have lowercased the input.
fn is_valid_hostname(s: &str) -> bool {
    if s.is_empty() || s.len() > 253 {
        return false;
    }
    for label in s.split('.') {
        if label.is_empty() || label.len() > 63 {
            return false;
        }
        let bytes = label.as_bytes();
        let first = bytes[0];
        if !(first.is_ascii_lowercase() || first.is_ascii_digit()) {
            return false;
        }
        for &b in &bytes[1..] {
            if !(b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-') {
                return false;
            }
        }
    }
    true
}

#[cfg(test)]
mod tests {
    use super::*;

    fn minimal() -> String {
        concat!(
            "owner: danlap\n",
            "max_mirrors: 5\n",
            "tags: []\n",
            "paused: false\n",
            "description: \"\"\n",
        )
        .to_string()
    }

    #[test]
    fn parses_minimal_valid() {
        let config = parse_str(&minimal()).unwrap();
        assert_eq!(config.owner, "danlap");
        assert_eq!(config.max_mirrors, 5);
        assert!(config.tags.is_empty());
        assert!(!config.paused);
        assert_eq!(config.description, "");
    }

    #[test]
    fn parses_full_valid() {
        let yaml = "\
owner: homeserver
max_mirrors: 7
tags: [personal, notes, work]
paused: true
description: \"a test repo\"
";
        let config = parse_str(yaml).unwrap();
        assert_eq!(config.owner, "homeserver");
        assert_eq!(config.max_mirrors, 7);
        assert_eq!(config.tags, vec!["personal", "notes", "work"]);
        assert!(config.paused);
        assert_eq!(config.description, "a test repo");
    }

    #[test]
    fn accepts_fqdn_owner() {
        let yaml = "\
owner: box1.example.com
max_mirrors: 5
tags: []
paused: false
description: \"\"
";
        assert_eq!(parse_str(yaml).unwrap().owner, "box1.example.com");
    }

    #[test]
    fn lowercases_owner() {
        let yaml = "\
owner: DanLap
max_mirrors: 5
tags: []
paused: false
description: \"\"
";
        assert_eq!(parse_str(yaml).unwrap().owner, "danlap");
    }

    #[test]
    fn rejects_missing_owner() {
        let yaml = "max_mirrors: 5\ntags: []\npaused: false\ndescription: \"\"\n";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    #[test]
    fn rejects_missing_max_mirrors() {
        let yaml = "owner: danlap\ntags: []\npaused: false\ndescription: \"\"\n";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    #[test]
    fn rejects_missing_tags() {
        let yaml = "owner: danlap\nmax_mirrors: 5\npaused: false\ndescription: \"\"\n";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    #[test]
    fn rejects_missing_paused() {
        let yaml = "owner: danlap\nmax_mirrors: 5\ntags: []\ndescription: \"\"\n";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    #[test]
    fn rejects_missing_description() {
        let yaml = "owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\n";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    #[test]
    fn rejects_unknown_field() {
        let yaml = "\
owner: danlap
max_mirrors: 5
tags: []
paused: false
description: \"\"
evil_field: yes
";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    #[test]
    fn rejects_invalid_owner_traversal() {
        let yaml = "\
owner: \"../../root\"
max_mirrors: 5
tags: []
paused: false
description: \"\"
";
        assert!(matches!(
            parse_str(yaml),
            Err(SidecarError::InvalidOwner(_))
        ));
    }

    #[test]
    fn rejects_empty_owner() {
        let yaml = "\
owner: \"\"
max_mirrors: 5
tags: []
paused: false
description: \"\"
";
        assert!(matches!(
            parse_str(yaml),
            Err(SidecarError::InvalidOwner(_))
        ));
    }

    #[test]
    fn rejects_negative_max_mirrors() {
        let yaml = "\
owner: danlap
max_mirrors: -1
tags: []
paused: false
description: \"\"
";
        assert!(matches!(
            parse_str(yaml),
            Err(SidecarError::NegativeMaxMirrors(_))
        ));
    }

    #[test]
    fn clamps_max_mirrors_below_floor() {
        let yaml = "\
owner: danlap
max_mirrors: 1
tags: []
paused: false
description: \"\"
";
        assert_eq!(parse_str(yaml).unwrap().max_mirrors, MIN_MIRRORS);
    }

    #[test]
    fn clamps_max_mirrors_above_ceiling() {
        let yaml = "\
owner: danlap
max_mirrors: 100
tags: []
paused: false
description: \"\"
";
        assert_eq!(parse_str(yaml).unwrap().max_mirrors, MAX_MIRRORS);
    }

    #[test]
    fn accepts_exactly_min_mirrors() {
        let yaml = "\
owner: danlap
max_mirrors: 3
tags: []
paused: false
description: \"\"
";
        assert_eq!(parse_str(yaml).unwrap().max_mirrors, 3);
    }

    #[test]
    fn accepts_exactly_max_mirrors() {
        let yaml = "\
owner: danlap
max_mirrors: 10
tags: []
paused: false
description: \"\"
";
        assert_eq!(parse_str(yaml).unwrap().max_mirrors, 10);
    }

    #[test]
    fn rejects_too_many_tags() {
        let mut tags = String::new();
        for i in 0..17 {
            tags.push_str(&format!("  - t{}\n", i));
        }
        let yaml = format!(
            "owner: danlap\nmax_mirrors: 5\ntags:\n{}paused: false\ndescription: \"\"\n",
            tags
        );
        assert!(matches!(
            parse_str(&yaml),
            Err(SidecarError::TooManyTags(17))
        ));
    }

    #[test]
    fn rejects_empty_tag() {
        let yaml = "\
owner: danlap
max_mirrors: 5
tags: [\"\"]
paused: false
description: \"\"
";
        assert!(matches!(
            parse_str(yaml),
            Err(SidecarError::InvalidTag(_))
        ));
    }

    #[test]
    fn rejects_too_long_tag() {
        let long_tag = "x".repeat(MAX_TAG_LEN + 1);
        let yaml = format!(
            "owner: danlap\nmax_mirrors: 5\ntags: [\"{}\"]\npaused: false\ndescription: \"\"\n",
            long_tag
        );
        assert!(matches!(
            parse_str(&yaml),
            Err(SidecarError::InvalidTag(_))
        ));
    }

    #[test]
    fn rejects_too_long_description() {
        let long_desc = "x".repeat(MAX_DESCRIPTION_LEN + 1);
        let yaml = format!(
            "owner: danlap\nmax_mirrors: 5\ntags: []\npaused: false\ndescription: \"{}\"\n",
            long_desc
        );
        assert!(matches!(
            parse_str(&yaml),
            Err(SidecarError::DescriptionTooLong(_))
        ));
    }

    #[test]
    fn rejects_oversized_file() {
        let huge = "x".repeat(MAX_SIDECAR_BYTES as usize + 1);
        assert!(matches!(parse_str(&huge), Err(SidecarError::TooLarge(_))));
    }

    #[test]
    fn rejects_malformed_yaml() {
        let yaml = "owner: [\nmax_mirrors: 5\n";
        assert!(matches!(parse_str(yaml), Err(SidecarError::Parse(_))));
    }

    // Hostname validation --------------------------------------------------

    #[test]
    fn hostname_accepts_simple() {
        assert!(is_valid_hostname("danlap"));
        assert!(is_valid_hostname("home-server"));
        assert!(is_valid_hostname("box-1"));
        assert!(is_valid_hostname("a"));
    }

    #[test]
    fn hostname_accepts_fqdn() {
        assert!(is_valid_hostname("danlap.local"));
        assert!(is_valid_hostname("homeserver.lan"));
        assert!(is_valid_hostname("box1.example.com"));
    }

    #[test]
    fn hostname_rejects_invalid() {
        assert!(!is_valid_hostname(""));
        assert!(!is_valid_hostname("."));
        assert!(!is_valid_hostname(".foo"));
        assert!(!is_valid_hostname("foo."));
        assert!(!is_valid_hostname("foo..bar"));
        assert!(!is_valid_hostname("-foo"));
        assert!(!is_valid_hostname("foo_bar"));
        assert!(!is_valid_hostname("foo bar"));
        assert!(!is_valid_hostname("../../root"));
        assert!(!is_valid_hostname("foo/bar"));
    }
}
