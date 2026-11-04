// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! JSON file executor — manages individual keys in JSON files idempotently.

use super::atomic;
use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct JsonFileExecutor;

impl Executor for JsonFileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("target_path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("json_file", &path);

        if path.is_empty() {
            result.error = Some("json_file executor: target_path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let key = params.get("key").cloned().unwrap_or_default();
        if key.is_empty() {
            result.error = Some("json_file executor: key param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let indent = params.get("indent").map(|s| s.as_str()).unwrap_or("  ");

        // Read existing file or start with empty object. Only a missing file
        // (or an empty one) starts empty: a read error or content that isn't
        // JSON fails the resource rather than being overwritten with `{}` +
        // this one key.
        let raw = match super::read_for_edit(&path) {
            Ok(c) => c,
            Err(e) => {
                result.error = Some(e);
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        };
        let mut doc: serde_json::Value = if raw.trim().is_empty() {
            serde_json::Value::Object(serde_json::Map::new())
        } else {
            match serde_json::from_str(&raw) {
                Ok(v) => v,
                Err(e) => {
                    result.error = Some(format!("parse {} as JSON: {} (refusing to rewrite it)", path, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        };

        let segments: Vec<&str> = key.split('.').collect();

        match state {
            "present" => {
                let value_str = params.get("value").cloned().unwrap_or_default();
                if value_str.is_empty() {
                    result.error = Some("json_file executor: value param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let desired: serde_json::Value = match serde_json::from_str(&value_str) {
                    Ok(v) => v,
                    Err(e) => {
                        result.error = Some(format!("parse value {:?}: {}", value_str, e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                };

                // Check current value.
                let current = get_nested(&doc, &segments);
                if current == Some(&desired) {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let file_existed = std::path::Path::new(&path).exists();
                result.action = if file_existed && current.is_some() {
                    Action::Updated
                } else {
                    Action::Created
                };
                result.changed = true;

                if !dry_run {
                    set_nested(&mut doc, &segments, desired);
                    if let Err(e) = write_json(&path, &doc, indent) {
                        result.error = Some(e);
                    }
                }
            }
            "absent" => {
                let current = get_nested(&doc, &segments);
                if current.is_none() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if !dry_run {
                    remove_nested(&mut doc, &segments);
                    if let Err(e) = write_json(&path, &doc, indent) {
                        result.error = Some(e);
                    }
                }
            }
            _ => {
                result.error = Some(format!("json_file executor: unknown state {:?}", state));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn get_nested<'a>(doc: &'a serde_json::Value, segments: &[&str]) -> Option<&'a serde_json::Value> {
    let mut current = doc;
    for seg in segments {
        match current {
            serde_json::Value::Object(map) => {
                current = map.get(*seg)?;
            }
            serde_json::Value::Array(arr) => {
                let idx: usize = seg.parse().ok()?;
                current = arr.get(idx)?;
            }
            _ => return None,
        }
    }
    Some(current)
}

fn set_nested(doc: &mut serde_json::Value, segments: &[&str], value: serde_json::Value) {
    if segments.is_empty() {
        return;
    }
    if segments.len() == 1 {
        if let serde_json::Value::Object(map) = doc {
            map.insert(segments[0].to_string(), value);
        }
        return;
    }

    // Ensure intermediate maps exist.
    let seg = segments[0];
    if let serde_json::Value::Object(map) = doc {
        if !map.contains_key(seg) {
            map.insert(
                seg.to_string(),
                serde_json::Value::Object(serde_json::Map::new()),
            );
        }
        if let Some(child) = map.get_mut(seg) {
            set_nested(child, &segments[1..], value);
        }
    }
}

fn remove_nested(doc: &mut serde_json::Value, segments: &[&str]) {
    if segments.is_empty() {
        return;
    }
    if segments.len() == 1 {
        if let serde_json::Value::Object(map) = doc {
            map.remove(segments[0]);
        }
        return;
    }

    let seg = segments[0];
    if let serde_json::Value::Object(map) = doc {
        if let Some(child) = map.get_mut(seg) {
            remove_nested(child, &segments[1..]);
        }
    }
}

fn write_json(path: &str, doc: &serde_json::Value, indent: &str) -> Result<(), String> {
    let formatted = if indent == "  " {
        serde_json::to_string_pretty(doc).map_err(|e| format!("serialize json: {}", e))?
    } else {
        // Custom indent requires manual formatting.
        let buf = Vec::new();
        let formatter = serde_json::ser::PrettyFormatter::with_indent(indent.as_bytes());
        let mut ser = serde_json::Serializer::with_formatter(buf, formatter);
        serde::Serialize::serialize(doc, &mut ser).map_err(|e| format!("serialize json: {}", e))?;
        String::from_utf8(ser.into_inner()).map_err(|e| format!("json to utf8: {}", e))?
    };

    let output = format!("{}\n", formatted);
    atomic::atomic_write(path, &output).map(|_| ())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn missing_path_errors() {
        let exec = JsonFileExecutor;
        let params = make_params(&[("key", "port"), ("value", "80")]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("target_path param is required"));
    }

    #[test]
    fn missing_key_errors() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        std::fs::write(&path, "{}").unwrap();

        let exec = JsonFileExecutor;
        let params = make_params(&[("target_path", path.to_str().unwrap())]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("key param is required"));
    }

    #[test]
    fn present_creates_key() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        // File does not exist yet — executor should create it.

        let exec = JsonFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("key", "app.name"),
            ("value", "\"vigo\""),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let content = std::fs::read_to_string(&path).unwrap();
        let doc: serde_json::Value = serde_json::from_str(&content).unwrap();
        assert_eq!(doc["app"]["name"], "vigo");
    }

    #[test]
    fn present_updates_key() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        std::fs::write(&path, "{\"port\": 80}\n").unwrap();

        let exec = JsonFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("key", "port"),
            ("value", "90"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);

        let content = std::fs::read_to_string(&path).unwrap();
        let doc: serde_json::Value = serde_json::from_str(&content).unwrap();
        assert_eq!(doc["port"], 90);
    }

    #[test]
    fn present_key_already_matches() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        std::fs::write(&path, "{\"port\": 80}\n").unwrap();

        let exec = JsonFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("key", "port"),
            ("value", "80"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn absent_removes_key() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        std::fs::write(&path, "{\"port\": 80, \"host\": \"localhost\"}\n").unwrap();

        let exec = JsonFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("key", "port"),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let content = std::fs::read_to_string(&path).unwrap();
        let doc: serde_json::Value = serde_json::from_str(&content).unwrap();
        assert!(doc.get("port").is_none());
        assert_eq!(doc["host"], "localhost");
    }

    #[test]
    fn absent_key_not_found() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        std::fs::write(&path, "{\"host\": \"localhost\"}\n").unwrap();

        let exec = JsonFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("key", "nonexistent"),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);
    }

    #[test]
    fn dry_run_does_not_write() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.json");
        let original = "{\"port\": 80}\n";
        std::fs::write(&path, original).unwrap();

        let exec = JsonFileExecutor;
        let params = make_params(&[
            ("target_path", path.to_str().unwrap()),
            ("key", "port"),
            ("value", "443"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, true);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);

        // File should be unchanged because of dry_run.
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    /// Unparseable JSON is not "empty": rewriting it as `{key: value}` would
    /// discard the whole document.
    #[test]
    fn unparseable_json_fails_and_is_left_intact() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("c.json");
        std::fs::write(&path, "{ not json").unwrap();
        let params: HashMap<String, String> = [("target_path", path.to_str().unwrap()), ("key", "a"), ("value", "1")]
            .iter().map(|(k, v)| (k.to_string(), v.to_string())).collect();
        let r = JsonFileExecutor.execute(&params, false);
        assert!(r.error.is_some());
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "{ not json");
    }
}
