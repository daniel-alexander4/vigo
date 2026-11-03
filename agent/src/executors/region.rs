// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Region computation — constrains file operations to a section between two
//! regex anchors. Used by replace, field_edit, and file_line executors.

use regex_lite::Regex;

/// Compute the inclusive line range `(start_idx, end_idx)` bounded by optional
/// regex anchors. Defaults to the full file when both are `None`.
///
/// - `region_start` only: from first match to end of file.
/// - `region_end` only: from start of file to first match.
/// - Both: from first match of start to first match of end *after* start.
pub fn compute_region(
    lines: &[String],
    region_start: Option<&str>,
    region_end: Option<&str>,
) -> Result<(usize, usize), String> {
    if lines.is_empty() {
        return Ok((0, 0));
    }

    let start_idx = match region_start {
        Some(pat) => {
            let re = Regex::new(pat)
                .map_err(|e| format!("invalid region_start regex {:?}: {}", pat, e))?;
            lines
                .iter()
                .position(|l| re.is_match(l))
                .ok_or_else(|| format!("region_start {:?} not found in file", pat))?
        }
        None => 0,
    };

    let end_idx = match region_end {
        Some(pat) => {
            let re = Regex::new(pat)
                .map_err(|e| format!("invalid region_end regex {:?}: {}", pat, e))?;
            // Search from start_idx forward.
            lines[start_idx..]
                .iter()
                .position(|l| re.is_match(l))
                .map(|i| i + start_idx)
                .ok_or_else(|| format!("region_end {:?} not found in file", pat))?
        }
        None => lines.len() - 1,
    };

    if end_idx < start_idx {
        return Err(format!(
            "region_end (line {}) is before region_start (line {})",
            end_idx, start_idx
        ));
    }

    Ok((start_idx, end_idx))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn lines(s: &str) -> Vec<String> {
        s.lines().map(|l| l.to_string()).collect()
    }

    #[test]
    fn full_file_when_no_anchors() {
        let l = lines("a\nb\nc");
        let (s, e) = compute_region(&l, None, None).unwrap();
        assert_eq!((s, e), (0, 2));
    }

    #[test]
    fn start_only() {
        let l = lines("header\n# Auth\nPermitRoot yes\nMaxAuth 3");
        let (s, e) = compute_region(&l, Some("^# Auth"), None).unwrap();
        assert_eq!(s, 1);
        assert_eq!(e, 3);
    }

    #[test]
    fn end_only() {
        let l = lines("a\nb\n# END\nfooter");
        let (s, e) = compute_region(&l, None, Some("^# END")).unwrap();
        assert_eq!(s, 0);
        assert_eq!(e, 2);
    }

    #[test]
    fn both_anchors() {
        let l = lines("header\n[section]\nkey=val\n[other]\nfooter");
        let (s, e) = compute_region(&l, Some("^\\[section\\]"), Some("^\\[other\\]")).unwrap();
        assert_eq!((s, e), (1, 3));
    }

    #[test]
    fn start_not_found() {
        let l = lines("a\nb\nc");
        let err = compute_region(&l, Some("^MISSING"), None).unwrap_err();
        assert!(err.contains("not found"));
    }

    #[test]
    fn end_not_found() {
        let l = lines("a\nb\nc");
        let err = compute_region(&l, None, Some("^MISSING")).unwrap_err();
        assert!(err.contains("not found"));
    }

    #[test]
    fn empty_file() {
        let l: Vec<String> = Vec::new();
        let (s, e) = compute_region(&l, None, None).unwrap();
        assert_eq!((s, e), (0, 0));
    }

    #[test]
    fn invalid_regex() {
        let l = lines("a\nb");
        let err = compute_region(&l, Some("[invalid"), None).unwrap_err();
        assert!(err.contains("invalid region_start regex"));
    }
}
