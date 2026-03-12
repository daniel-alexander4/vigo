// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! JSON file executor — manages individual keys in JSON files idempotently.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct JsonFileExecutor;

impl Executor for JsonFileExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("json_file", &path);

        if path.is_empty() {
            result.error = Some("json_file executor: path param is required".into());
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

        // Read existing file or start with empty object.
        let mut doc: serde_json::Value = std::fs::read_to_string(&path)
            .ok()
            .and_then(|s| serde_json::from_str(&s).ok())
            .unwrap_or(serde_json::Value::Object(serde_json::Map::new()));

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
    std::fs::write(path, output).map_err(|e| format!("write {}: {}", path, e))
}
