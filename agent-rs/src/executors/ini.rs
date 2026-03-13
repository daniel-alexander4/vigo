// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! INI file executor — manages key-value pairs in INI files idempotently.

use super::{Action, Executor, ResourceResult};
use std::collections::HashMap;
use std::time::Instant;

pub struct IniExecutor;

#[derive(Clone)]
enum IniElement {
    Comment(String),
    Section(String),
    KeyValue(String, String, String), // key, value, separator
    Blank,
}

impl Executor for IniExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let path = params.get("path").cloned().unwrap_or_default();
        let mut result = ResourceResult::new("ini", &path);

        if path.is_empty() {
            result.error = Some("ini executor: path param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let section = params.get("section").cloned().unwrap_or_default();
        let key = params.get("key").cloned().unwrap_or_default();
        if key.is_empty() {
            result.error = Some("ini executor: key param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let sep = params.get("separator").map(|s| s.as_str()).unwrap_or("=");
        let value = params.get("value").cloned().unwrap_or_default();

        let content = std::fs::read_to_string(&path).unwrap_or_default();
        let mut elements = parse_ini(&content, sep);

        match state {
            "present" => {
                if value.is_empty() {
                    result.error = Some("ini executor: value param is required for state=present".into());
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                match set_ini_value(&mut elements, &section, &key, &value, sep) {
                    IniChange::None => {
                        result.action = Action::None;
                    }
                    IniChange::Updated => {
                        result.action = Action::Updated;
                        result.changed = true;
                    }
                    IniChange::Created => {
                        result.action = Action::Created;
                        result.changed = true;
                    }
                }
            }
            "absent" => {
                if remove_ini_key(&mut elements, &section, &key) {
                    result.action = Action::Deleted;
                    result.changed = true;
                } else {
                    result.action = Action::None;
                }
            }
            _ => {
                result.error = Some(format!("ini executor: unknown state {:?}", state));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
        }

        if result.changed && !dry_run {
            let output = render_ini(&elements);
            if let Err(e) = std::fs::write(&path, output) {
                result.error = Some(format!("write {}: {}", path, e));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn parse_ini(content: &str, sep: &str) -> Vec<IniElement> {
    let mut elements = Vec::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            elements.push(IniElement::Blank);
        } else if trimmed.starts_with('#') || trimmed.starts_with(';') {
            elements.push(IniElement::Comment(line.to_string()));
        } else if trimmed.starts_with('[') && trimmed.ends_with(']') {
            let name = trimmed[1..trimmed.len() - 1].trim().to_string();
            elements.push(IniElement::Section(name));
        } else if let Some(pos) = line.find(sep) {
            let k = line[..pos].trim().to_string();
            let v = line[pos + sep.len()..].trim().to_string();
            elements.push(IniElement::KeyValue(k, v, sep.to_string()));
        } else {
            elements.push(IniElement::Comment(line.to_string()));
        }
    }
    elements
}

enum IniChange {
    None,
    Created,
    Updated,
}

fn set_ini_value(
    elements: &mut Vec<IniElement>,
    section: &str,
    key: &str,
    value: &str,
    sep: &str,
) -> IniChange {
    let mut in_section = section.is_empty(); // global = always in section
    let mut last_in_section = None;

    for (i, elem) in elements.iter_mut().enumerate() {
        match elem {
            IniElement::Section(name) => {
                if !section.is_empty() {
                    in_section = *name == section;
                } else {
                    in_section = false; // global section ends at first [section]
                }
            }
            IniElement::KeyValue(k, v, _) if in_section && *k == key => {
                if *v == value {
                    return IniChange::None;
                }
                *v = value.to_string();
                return IniChange::Updated;
            }
            _ => {}
        }
        if in_section {
            last_in_section = Some(i);
        }
    }

    // Key not found — insert.
    let new_elem = IniElement::KeyValue(key.to_string(), value.to_string(), sep.to_string());

    if !section.is_empty() {
        // Check if section exists.
        let section_exists = elements.iter().any(|e| matches!(e, IniElement::Section(s) if *s == section));
        if !section_exists {
            elements.push(IniElement::Blank);
            elements.push(IniElement::Section(section.to_string()));
            elements.push(new_elem);
        } else if let Some(idx) = last_in_section {
            elements.insert(idx + 1, new_elem);
        } else {
            elements.push(new_elem);
        }
    } else {
        // Global section: insert before first section header.
        let first_section = elements.iter().position(|e| matches!(e, IniElement::Section(_)));
        match first_section {
            Some(idx) => elements.insert(idx, new_elem),
            None => elements.push(new_elem),
        }
    }

    IniChange::Created
}

fn remove_ini_key(elements: &mut Vec<IniElement>, section: &str, key: &str) -> bool {
    let mut in_section = section.is_empty();
    let mut removed = false;

    elements.retain(|elem| {
        match elem {
            IniElement::Section(name) => {
                if !section.is_empty() {
                    in_section = *name == section;
                } else {
                    in_section = false;
                }
                true
            }
            IniElement::KeyValue(k, _, _) if in_section && *k == key => {
                removed = true;
                false
            }
            _ => true,
        }
    });

    removed
}

fn render_ini(elements: &[IniElement]) -> String {
    let mut lines = Vec::new();
    for elem in elements {
        match elem {
            IniElement::Comment(s) => lines.push(s.clone()),
            IniElement::Section(name) => lines.push(format!("[{}]", name)),
            IniElement::KeyValue(k, v, sep) => lines.push(format!("{} {} {}", k, sep, v)),
            IniElement::Blank => lines.push(String::new()),
        }
    }
    let mut output = lines.join("\n");
    if !output.ends_with('\n') {
        output.push('\n');
    }
    output
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn missing_path_errors() {
        let exec = IniExecutor;
        let params = make_params(&[("key", "port"), ("value", "80")]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("path param is required"));
    }

    #[test]
    fn missing_key_errors() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[app]\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[("path", path.to_str().unwrap())]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("key param is required"));
    }

    #[test]
    fn present_missing_value_errors() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[app]\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("key", "port"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("value param is required"));
    }

    #[test]
    fn present_creates_key_in_section() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[database]\nhost = localhost\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("section", "database"),
            ("key", "port"),
            ("value", "5432"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Created);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("port = 5432"));
        assert!(content.contains("[database]"));
    }

    #[test]
    fn present_updates_existing_key() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[app]\nport = 80\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("section", "app"),
            ("key", "port"),
            ("value", "443"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Updated);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("port = 443"));
        assert!(!content.contains("port = 80"));
    }

    #[test]
    fn present_key_already_matches() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[app]\nport = 443\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("section", "app"),
            ("key", "port"),
            ("value", "443"),
            ("state", "present"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(!result.changed);
        assert_eq!(result.action, Action::None);

        // File should be unchanged.
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, "[app]\nport = 443\n");
    }

    #[test]
    fn absent_removes_key() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[app]\nport = 80\nhost = localhost\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("section", "app"),
            ("key", "port"),
            ("state", "absent"),
        ]);
        let result = exec.execute(&params, false);
        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert!(result.changed);
        assert_eq!(result.action, Action::Deleted);

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(!content.contains("port"));
        assert!(content.contains("host = localhost"));
    }

    #[test]
    fn absent_key_not_found() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test.ini");
        std::fs::write(&path, "[app]\nhost = localhost\n").unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("section", "app"),
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
        let path = dir.path().join("test.ini");
        let original = "[app]\nport = 80\n";
        std::fs::write(&path, original).unwrap();

        let exec = IniExecutor;
        let params = make_params(&[
            ("path", path.to_str().unwrap()),
            ("section", "app"),
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
}
