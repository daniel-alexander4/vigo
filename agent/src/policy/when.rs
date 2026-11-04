// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! When expression parser, evaluator, and helpers.
//! Port of server/conditionals/when.go (501 lines).
//!
//! Grammar:
//!   or_expr  → and_expr ('or' and_expr)*
//!   and_expr → unary ('and' unary)*
//!   unary    → 'not' unary | primary
//!   primary  → 'true' | 'false' | ident | ident '(' args ')' | '(' or_expr ')'

use anyhow::{bail, Result};

/// Resolves a builtin function call (name + args) to a boolean.
type FuncResolver<'a> = &'a dyn Fn(&str, &[String]) -> Result<bool>;

// -- AST --

#[derive(Debug, Clone, PartialEq)]
pub enum WhenExpr {
    BoolLit(bool),
    VarRef(String),
    Not(Box<WhenExpr>),
    And(Box<WhenExpr>, Box<WhenExpr>),
    Or(Box<WhenExpr>, Box<WhenExpr>),
    FuncCall { name: String, args: Vec<String> },
}

// -- Lexer --

#[derive(Debug, Clone, PartialEq)]
enum TokenKind {
    Eof,
    Ident(String),
    True,
    False,
    And,
    Or,
    Not,
    LParen,
    RParen,
    StringLit(String),
    Comma,
}

#[derive(Debug, Clone)]
struct Token {
    kind: TokenKind,
}

fn lex(input: &str) -> Result<Vec<Token>> {
    let chars: Vec<char> = input.chars().collect();
    let mut pos = 0;
    let mut tokens = Vec::new();

    loop {
        // Skip whitespace
        while pos < chars.len() && chars[pos].is_whitespace() {
            pos += 1;
        }
        if pos >= chars.len() {
            tokens.push(Token { kind: TokenKind::Eof });
            return Ok(tokens);
        }

        let ch = chars[pos];
        match ch {
            '(' => {
                tokens.push(Token { kind: TokenKind::LParen });
                pos += 1;
            }
            ')' => {
                tokens.push(Token { kind: TokenKind::RParen });
                pos += 1;
            }
            ',' => {
                tokens.push(Token { kind: TokenKind::Comma });
                pos += 1;
            }
            '\'' => {
                // String literal
                pos += 1; // skip opening quote
                let start = pos;
                while pos < chars.len() {
                    if chars[pos] == '\'' {
                        let s: String = chars[start..pos].iter().collect();
                        pos += 1; // skip closing quote
                        tokens.push(Token { kind: TokenKind::StringLit(s) });
                        break;
                    }
                    if chars[pos] == '\\' && pos + 1 < chars.len() {
                        pos += 2;
                        continue;
                    }
                    pos += 1;
                }
                if pos > chars.len() || (pos == chars.len() && tokens.last().map(|t| matches!(t.kind, TokenKind::StringLit(_))) != Some(true)) {
                    bail!("unterminated string literal starting at position {}", start - 1);
                }
            }
            '!' => {
                tokens.push(Token { kind: TokenKind::Not });
                pos += 1;
            }
            // `&&` / `||` alongside the word forms. server/conditionals/when.go
            // lexes both (tokAnd/tokOr), the docs use the symbolic form, and the
            // fleet's configcrates are written with it — an expression the
            // server can't fully resolve (agent-side builtins, or the `changed`
            // sentinel) is handed to the agent verbatim, so this side has to
            // accept the same grammar or the resource is dropped on a parse
            // error it never had server-side.
            '&' => {
                if pos + 1 < chars.len() && chars[pos + 1] == '&' {
                    tokens.push(Token { kind: TokenKind::And });
                    pos += 2;
                } else {
                    bail!("expected '&&' at position {}, found single '&'", pos);
                }
            }
            '|' => {
                if pos + 1 < chars.len() && chars[pos + 1] == '|' {
                    tokens.push(Token { kind: TokenKind::Or });
                    pos += 2;
                } else {
                    bail!("expected '||' at position {}, found single '|'", pos);
                }
            }
            _ if ch.is_alphabetic() || ch == '_' => {
                let start = pos;
                while pos < chars.len() && (chars[pos].is_alphanumeric() || chars[pos] == '_') {
                    pos += 1;
                }
                let word: String = chars[start..pos].iter().collect();
                let kind = match word.as_str() {
                    "true" => TokenKind::True,
                    "false" => TokenKind::False,
                    "and" => TokenKind::And,
                    "or" => TokenKind::Or,
                    "not" => TokenKind::Not,
                    _ => TokenKind::Ident(word),
                };
                tokens.push(Token { kind });
            }
            _ => bail!("unexpected character {:?} at position {}", ch, pos),
        }
    }
}

// -- Parser --

struct Parser {
    tokens: Vec<Token>,
    pos: usize,
}

impl Parser {
    fn current(&self) -> &TokenKind {
        if self.pos >= self.tokens.len() {
            &TokenKind::Eof
        } else {
            &self.tokens[self.pos].kind
        }
    }

    fn advance(&mut self) -> TokenKind {
        let kind = self.current().clone();
        self.pos += 1;
        kind
    }

    fn parse_or_expr(&mut self) -> Result<WhenExpr> {
        let mut left = self.parse_and_expr()?;
        while matches!(self.current(), TokenKind::Or) {
            self.advance();
            let right = self.parse_and_expr()?;
            left = WhenExpr::Or(Box::new(left), Box::new(right));
        }
        Ok(left)
    }

    fn parse_and_expr(&mut self) -> Result<WhenExpr> {
        let mut left = self.parse_unary()?;
        while matches!(self.current(), TokenKind::And) {
            self.advance();
            let right = self.parse_unary()?;
            left = WhenExpr::And(Box::new(left), Box::new(right));
        }
        Ok(left)
    }

    fn parse_unary(&mut self) -> Result<WhenExpr> {
        if matches!(self.current(), TokenKind::Not) {
            self.advance();
            let operand = self.parse_unary()?;
            return Ok(WhenExpr::Not(Box::new(operand)));
        }
        self.parse_primary()
    }

    fn parse_primary(&mut self) -> Result<WhenExpr> {
        match self.current().clone() {
            TokenKind::True => {
                self.advance();
                Ok(WhenExpr::BoolLit(true))
            }
            TokenKind::False => {
                self.advance();
                Ok(WhenExpr::BoolLit(false))
            }
            TokenKind::Ident(name) => {
                self.advance();
                if matches!(self.current(), TokenKind::LParen) {
                    self.parse_func_call(name)
                } else {
                    Ok(WhenExpr::VarRef(name))
                }
            }
            TokenKind::LParen => {
                self.advance();
                let expr = self.parse_or_expr()?;
                if !matches!(self.current(), TokenKind::RParen) {
                    bail!("expected ')' but got {:?}", self.current());
                }
                self.advance();
                Ok(expr)
            }
            ref other => bail!("unexpected token {:?}", other),
        }
    }

    fn parse_func_call(&mut self, name: String) -> Result<WhenExpr> {
        self.advance(); // skip '('
        let mut args = Vec::new();
        while !matches!(self.current(), TokenKind::RParen) {
            if !args.is_empty() {
                if !matches!(self.current(), TokenKind::Comma) {
                    bail!("expected ',' between function arguments");
                }
                self.advance();
            }
            match self.current().clone() {
                TokenKind::StringLit(s) => {
                    self.advance();
                    args.push(s);
                }
                _ => bail!("function arguments must be single-quoted strings"),
            }
        }
        if !matches!(self.current(), TokenKind::RParen) {
            bail!("expected ')' after function arguments");
        }
        self.advance();
        Ok(WhenExpr::FuncCall { name, args })
    }
}

/// Parse a when expression string into an AST.
/// Empty string returns BoolLit(true) (always run).
pub fn parse(input: &str) -> Result<WhenExpr> {
    let input = input.trim();
    if input.is_empty() {
        return Ok(WhenExpr::BoolLit(true));
    }

    let tokens = lex(input)?;
    let mut parser = Parser { tokens, pos: 0 };
    let expr = parser.parse_or_expr()?;

    if !matches!(parser.current(), TokenKind::Eof) {
        bail!("unexpected token {:?} at end of expression", parser.current());
    }

    Ok(expr)
}

/// Evaluate a when expression using a variable resolver.
/// Returns error if function calls are encountered (use eval_with_funcs instead).
pub fn eval(expr: &WhenExpr, resolve: &dyn Fn(&str) -> bool) -> Result<bool> {
    eval_with_funcs(expr, resolve, None)
}

/// Evaluate with optional function resolver for builtins.
pub fn eval_with_funcs(
    expr: &WhenExpr,
    resolve: &dyn Fn(&str) -> bool,
    funcs: Option<FuncResolver>,
) -> Result<bool> {
    match expr {
        WhenExpr::BoolLit(v) => Ok(*v),
        WhenExpr::VarRef(name) => Ok(resolve(name)),
        WhenExpr::Not(operand) => {
            let val = eval_with_funcs(operand, resolve, funcs)?;
            Ok(!val)
        }
        WhenExpr::And(left, right) => {
            let l = eval_with_funcs(left, resolve, funcs)?;
            if !l {
                return Ok(false);
            }
            eval_with_funcs(right, resolve, funcs)
        }
        WhenExpr::Or(left, right) => {
            let l = eval_with_funcs(left, resolve, funcs)?;
            if l {
                return Ok(true);
            }
            eval_with_funcs(right, resolve, funcs)
        }
        WhenExpr::FuncCall { name, args } => match funcs {
            Some(f) => f(name, args),
            None => bail!("function call {:?} not supported in this context", name),
        },
    }
}

/// Returns true if the expression contains any function calls.
pub fn contains_func_calls(expr: &WhenExpr) -> bool {
    match expr {
        WhenExpr::BoolLit(_) | WhenExpr::VarRef(_) => false,
        WhenExpr::Not(operand) => contains_func_calls(operand),
        WhenExpr::And(left, right) | WhenExpr::Or(left, right) => {
            contains_func_calls(left) || contains_func_calls(right)
        }
        WhenExpr::FuncCall { .. } => true,
    }
}

/// The identifier reserved for the runner's notify/subscribes trigger.
///
/// It is deliberately NOT a var: nothing ever puts it in the vars map, and the
/// runner decides its value at execution time from whether the resource was
/// woken by a notify. Anything evaluating a `when:` expression before that
/// point must treat it as unknown rather than looking it up.
pub const CHANGED_SENTINEL: &str = "changed";

/// True if the expression gates on the `changed` sentinel.
///
/// Use this instead of a substring test on the raw expression — `contains
/// ("changed")` also matches an ordinary var like `unchanged_files`, which
/// would silently turn a normal conditional into a trigger-only resource.
pub fn references_changed(expr: &WhenExpr) -> bool {
    match expr {
        WhenExpr::BoolLit(_) | WhenExpr::FuncCall { .. } => false,
        WhenExpr::VarRef(name) => name == CHANGED_SENTINEL,
        WhenExpr::Not(operand) => references_changed(operand),
        WhenExpr::And(left, right) | WhenExpr::Or(left, right) => {
            references_changed(left) || references_changed(right)
        }
    }
}

/// Replace all VarRef nodes with BoolLit based on vars map.
/// Function calls are unchanged.
pub fn substitute_vars(
    expr: &WhenExpr,
    vars: &serde_json::Map<String, serde_json::Value>,
) -> WhenExpr {
    match expr {
        WhenExpr::BoolLit(_) => expr.clone(),
        WhenExpr::VarRef(name) => WhenExpr::BoolLit(when_var_is_true(name, vars)),
        WhenExpr::Not(operand) => WhenExpr::Not(Box::new(substitute_vars(operand, vars))),
        WhenExpr::And(left, right) => WhenExpr::And(
            Box::new(substitute_vars(left, vars)),
            Box::new(substitute_vars(right, vars)),
        ),
        WhenExpr::Or(left, right) => WhenExpr::Or(
            Box::new(substitute_vars(left, vars)),
            Box::new(substitute_vars(right, vars)),
        ),
        WhenExpr::FuncCall { .. } => expr.clone(),
    }
}

/// Returns true only if vars[name] == "true" (string exact match).
pub fn when_var_is_true(name: &str, vars: &serde_json::Map<String, serde_json::Value>) -> bool {
    vars.get(name)
        .and_then(|v| v.as_str())
        .map(|s| s == "true")
        .unwrap_or(false)
}

/// Tristate evaluation: function calls are "unknown" (returns None).
/// Returns Some(bool) if the result can be determined through short-circuit logic.
pub fn tristate_eval(expr: &WhenExpr) -> Option<bool> {
    match expr {
        WhenExpr::BoolLit(v) => Some(*v),
        WhenExpr::VarRef(_) => Some(false),
        WhenExpr::Not(operand) => tristate_eval(operand).map(|v| !v),
        WhenExpr::And(left, right) => {
            let l = tristate_eval(left);
            if l == Some(false) {
                return Some(false); // false and ? = false
            }
            let r = tristate_eval(right);
            if r == Some(false) {
                return Some(false); // ? and false = false
            }
            match (l, r) {
                (Some(a), Some(b)) => Some(a && b),
                _ => None,
            }
        }
        WhenExpr::Or(left, right) => {
            let l = tristate_eval(left);
            if l == Some(true) {
                return Some(true); // true or ? = true
            }
            let r = tristate_eval(right);
            if r == Some(true) {
                return Some(true); // ? or true = true
            }
            match (l, r) {
                (Some(a), Some(b)) => Some(a || b),
                _ => None,
            }
        }
        WhenExpr::FuncCall { .. } => None, // unknown
    }
}

/// Convert AST back to string representation.
pub fn expr_string(expr: &WhenExpr) -> String {
    match expr {
        WhenExpr::BoolLit(true) => "true".to_string(),
        WhenExpr::BoolLit(false) => "false".to_string(),
        WhenExpr::VarRef(name) => name.clone(),
        WhenExpr::Not(operand) => {
            match operand.as_ref() {
                WhenExpr::And(_, _) | WhenExpr::Or(_, _) => {
                    format!("not ({})", expr_string(operand))
                }
                _ => format!("not {}", expr_string(operand)),
            }
        }
        WhenExpr::And(left, right) => {
            let l = if matches!(left.as_ref(), WhenExpr::Or(_, _)) {
                format!("({})", expr_string(left))
            } else {
                expr_string(left)
            };
            let r = if matches!(right.as_ref(), WhenExpr::Or(_, _)) {
                format!("({})", expr_string(right))
            } else {
                expr_string(right)
            };
            format!("{} and {}", l, r)
        }
        WhenExpr::Or(left, right) => {
            format!("{} or {}", expr_string(left), expr_string(right))
        }
        WhenExpr::FuncCall { name, args } => {
            let quoted: Vec<String> = args.iter().map(|a| format!("'{}'", a)).collect();
            format!("{}({})", name, quoted.join(", "))
        }
    }
}

// -- Agent-side builtin function evaluator --

use serde_json::Value;
use std::path::Path;
#[cfg(windows)]
use std::process::Command;

/// Evaluate a builtin function call on the agent.
///
/// Returns Ok(bool) for known builtins, Err for unknown names or wrong arity.
/// The caller should treat errors as "skip" (fail-safe).
pub fn evaluate_builtin(name: &str, args: &[String], traits: &Value) -> Result<bool> {
    match name {
        "file_exists" => {
            check_arity(name, args, 1)?;
            Ok(Path::new(&args[0]).is_file())
        }
        "dir_exists" => {
            check_arity(name, args, 1)?;
            Ok(Path::new(&args[0]).is_dir())
        }
        "command_succeeds" => {
            check_arity(name, args, 1)?;
            Ok(command_succeeds(&args[0]))
        }
        "process_running" => {
            check_arity(name, args, 1)?;
            Ok(process_running(&args[0]))
        }
        "hour_range" => {
            check_arity(name, args, 2)?;
            let start: u32 = args[0].parse().map_err(|_| anyhow::anyhow!("hour_range: invalid start hour {:?}", args[0]))?;
            let end: u32 = args[1].parse().map_err(|_| anyhow::anyhow!("hour_range: invalid end hour {:?}", args[1]))?;
            Ok(hour_in_range(local_hour(), start, end))
        }
        "minute_range" => {
            check_arity(name, args, 2)?;
            let start: u32 = args[0].parse().map_err(|_| anyhow::anyhow!("minute_range: invalid start minute {:?}", args[0]))?;
            let end: u32 = args[1].parse().map_err(|_| anyhow::anyhow!("minute_range: invalid end minute {:?}", args[1]))?;
            Ok(minute_in_range(local_minute(), start, end))
        }
        "day_of_week" => {
            check_arity(name, args, 1)?;
            Ok(day_of_week_matches(&args[0]))
        }
        "day_of_month" => {
            check_arity(name, args, 1)?;
            let target: u32 = args[0].parse().map_err(|_| anyhow::anyhow!("day_of_month: invalid day {:?}", args[0]))?;
            Ok(local_day_of_month() == target)
        }
        "flag_set" => {
            check_arity(name, args, 1)?;
            let path = crate::platform::paths::default_flags_dir().join(&args[0]);
            Ok(path.exists())
        }
        // has_command / has_service / has_package never paste their argument
        // into a shell string: a `when:` arg is operator/var-controlled text,
        // and `has_command('x; rm -rf /')` used to run the `rm`. The arg goes
        // in as its own argv entry (after `--`, so a leading `-` can't be read
        // as an option); has_command passes it to `sh` as positional `$1`.
        "has_command" => {
            check_arity(name, args, 1)?;
            #[cfg(unix)]
            {
                Ok(run_succeeds("/bin/sh", &["-c", "command -v -- \"$1\" >/dev/null 2>&1", "sh", &args[0]]))
            }
            #[cfg(not(unix))]
            {
                Ok(false)
            }
        }
        "has_service" => {
            check_arity(name, args, 1)?;
            let unit = format!("{}.service", &args[0]);
            Ok(run_succeeds("systemctl", &["list-unit-files", "--", &unit]))
        }
        "has_package" => {
            check_arity(name, args, 1)?;
            Ok(run_succeeds("dpkg", &["-l", "--", &args[0]]) || run_succeeds("rpm", &["-q", "--", &args[0]]))
        }
        "os" => {
            check_arity(name, args, 1)?;
            Ok(trait_eq_ci(traits, "/os/distro", &args[0]))
        }
        "os_family" => {
            check_arity(name, args, 1)?;
            let arg = args[0].to_lowercase();
            // "linux" matches any Linux-based family (debian, rhel, suse, etc.)
            if arg == "linux" {
                let family = trait_str(traits, "/os/family").unwrap_or("");
                Ok(!family.is_empty() && family != "windows" && family != "darwin" && family != "freebsd" && family != "openbsd" && family != "netbsd" && family != "illumos")
            } else {
                Ok(trait_eq_ci(traits, "/os/family", &args[0]))
            }
        }
        "distro" => {
            check_arity(name, args, 1)?;
            Ok(trait_eq_ci(traits, "/os/distro", &args[0]))
        }
        "arch" => {
            check_arity(name, args, 1)?;
            Ok(trait_str(traits, "/os/arch").map(|a| a == args[0]).unwrap_or(false))
        }
        "version_ge" => {
            check_arity(name, args, 1)?;
            let actual = trait_str(traits, "/os/version").unwrap_or("");
            Ok(version_ge(actual, &args[0]))
        }
        "in_group" => {
            check_arity(name, args, 1)?;
            Ok(in_group(&args[0]))
        }
        "has_display" => {
            let server = trait_str(traits, "/display/server").unwrap_or("none");
            Ok(server != "none")
        }
        "port_listening" => {
            check_arity(name, args, 1)?;
            let port: u16 = args[0]
                .parse()
                .map_err(|_| anyhow::anyhow!("{}: invalid port number {:?}", name, args[0]))?;
            if let Some(arr) = traits.pointer("/ports/listening").and_then(|v| v.as_array()) {
                Ok(arr.iter().any(|v| v.as_u64() == Some(port as u64)))
            } else {
                Ok(false)
            }
        }
        _ => bail!("unknown builtin function {:?}", name),
    }
}

fn check_arity(name: &str, args: &[String], expected: usize) -> Result<()> {
    if args.len() != expected {
        bail!("{}: expected {} argument(s), got {}", name, expected, args.len());
    }
    Ok(())
}

/// Ceiling for a `when:` builtin's probe subprocess. These run inline in the
/// convergence pass, per resource, so they are bounded like every other
/// agent subprocess (own process group, killed as a tree on expiry).
const WHEN_PROBE_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(30);

/// run_succeeds runs `prog args…` directly (no shell) under the agent's
/// bounded subprocess path; true iff it started and exited 0.
fn run_succeeds(prog: &str, args: &[&str]) -> bool {
    crate::executors::run_command_niced_timeout(prog, args, 0, WHEN_PROBE_TIMEOUT)
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// command_succeeds runs an operator-written shell command (`command_succeeds`
/// builtin — the shell is the point there).
fn command_succeeds(cmd: &str) -> bool {
    let (shell, shell_arg) = crate::platform::paths::shell_command();
    run_succeeds(shell, &[shell_arg, cmd])
}

#[cfg(target_os = "linux")]
fn process_running(name: &str) -> bool {
    let Ok(entries) = std::fs::read_dir("/proc") else { return false };
    for entry in entries.flatten() {
        let fname = entry.file_name();
        if fname.to_str().map(|s| s.chars().all(|c| c.is_ascii_digit())) != Some(true) {
            continue;
        }
        let comm = entry.path().join("comm");
        if let Ok(content) = std::fs::read_to_string(&comm) {
            if content.trim() == name {
                return true;
            }
        }
    }
    false
}

#[cfg(any(target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos"))]
fn process_running(name: &str) -> bool {
    run_succeeds("pgrep", &["-x", "--", name])
}

#[cfg(windows)]
fn process_running(name: &str) -> bool {
    // Use tasklist to check if process is running.
    let search = if name.ends_with(".exe") {
        name.to_string()
    } else {
        format!("{}.exe", name)
    };
    Command::new("tasklist")
        .args(["/FI", &format!("IMAGENAME eq {}", search), "/NH"])
        .output()
        .map(|o| {
            let stdout = String::from_utf8_lossy(&o.stdout);
            stdout.contains(&search)
        })
        .unwrap_or(false)
}

#[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "freebsd", target_os = "openbsd", target_os = "netbsd", target_os = "illumos", windows)))]
fn process_running(_name: &str) -> bool {
    false
}

/// Get local hour (0-23) using libc localtime.
fn local_hour() -> u32 {
    local_time().0
}

/// Get local minute (0-59).
fn local_minute() -> u32 {
    local_time().3
}

/// Get local day of month (1-31) using libc localtime.
fn local_day_of_month() -> u32 {
    local_time().2
}

/// Returns (hour, weekday 0=Sun..6=Sat, day_of_month, minute).
#[allow(deprecated)]
fn local_time() -> (u32, u32, u32, u32) {
    #[cfg(unix)]
    {
        use std::time::{SystemTime, UNIX_EPOCH};
        let secs = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs() as libc::time_t;
        let mut tm: libc::tm = unsafe { std::mem::zeroed() };
        unsafe { libc::localtime_r(&secs, &mut tm) };
        (tm.tm_hour as u32, tm.tm_wday as u32, tm.tm_mday as u32, tm.tm_min as u32)
    }
    #[cfg(not(unix))]
    {
        // Fallback: UTC-based. On Windows this loses timezone info but is safe.
        use std::time::{SystemTime, UNIX_EPOCH};
        let secs = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let days = secs / 86400;
        let time_of_day = secs % 86400;
        let hour = (time_of_day / 3600) as u32;
        let minute = ((time_of_day % 3600) / 60) as u32;
        // Jan 1 1970 was a Thursday (4).
        let wday = ((days + 4) % 7) as u32;
        // Full Gregorian calculation:
        let mut y = 1970i64;
        let mut remaining = days as i64;
        loop {
            let year_days = if is_leap(y) { 366 } else { 365 };
            if remaining < year_days { break; }
            remaining -= year_days;
            y += 1;
        }
        let leap = is_leap(y);
        let month_days: [i64; 12] = [
            31, if leap { 29 } else { 28 }, 31, 30, 31, 30,
            31, 31, 30, 31, 30, 31,
        ];
        let mut mday = remaining;
        for md in &month_days {
            if mday < *md { break; }
            mday -= md;
        }
        (hour, wday, (mday + 1) as u32, minute)
    }
}

#[cfg(not(unix))]
fn is_leap(y: i64) -> bool {
    (y % 4 == 0 && y % 100 != 0) || y % 400 == 0
}

fn hour_in_range(hour: u32, start: u32, end: u32) -> bool {
    if start <= end {
        hour >= start && hour < end // e.g., 9..17
    } else {
        hour >= start || hour < end // e.g., 22..4 (wraps midnight)
    }
}

fn minute_in_range(minute: u32, start: u32, end: u32) -> bool {
    if start <= end {
        minute >= start && minute < end // e.g., 0..30
    } else {
        minute >= start || minute < end // e.g., 45..15 (wraps hour)
    }
}

fn day_of_week_matches(target: &str) -> bool {
    let (_, wday, _, _) = local_time();
    let target_lower = target.to_lowercase();
    let current = match wday {
        0 => "sunday",
        1 => "monday",
        2 => "tuesday",
        3 => "wednesday",
        4 => "thursday",
        5 => "friday",
        6 => "saturday",
        _ => return false,
    };
    // Match full name or standard 3-letter abbreviation.
    current == target_lower || current[..3] == target_lower
}

fn trait_str<'a>(traits: &'a Value, pointer: &str) -> Option<&'a str> {
    traits.pointer(pointer).and_then(|v| v.as_str())
}

fn trait_eq_ci(traits: &Value, pointer: &str, expected: &str) -> bool {
    trait_str(traits, pointer)
        .map(|v| v.eq_ignore_ascii_case(expected))
        .unwrap_or(false)
}

/// Segment-wise numeric version comparison. "22.04" >= "22.04" → true.
fn version_ge(actual: &str, required: &str) -> bool {
    let a: Vec<u64> = actual.split('.').filter_map(|s| s.parse().ok()).collect();
    let r: Vec<u64> = required.split('.').filter_map(|s| s.parse().ok()).collect();
    for i in 0..r.len().max(a.len()) {
        let av = a.get(i).copied().unwrap_or(0);
        let rv = r.get(i).copied().unwrap_or(0);
        if av > rv { return true; }
        if av < rv { return false; }
    }
    true // equal
}

#[cfg(unix)]
fn in_group(group: &str) -> bool {
    // Try getent first (Linux, FreeBSD, OpenBSD, NetBSD).
    match crate::executors::run_command_niced_timeout("getent", &["group", "--", group], 0, WHEN_PROBE_TIMEOUT) {
        Ok(o) => o.status.success(),
        // Fallback for macOS (no getent): use dscl.
        Err(_) => run_succeeds("dscl", &[".", "-read", &format!("/Groups/{}", group)]),
    }
}

#[cfg(windows)]
fn in_group(group: &str) -> bool {
    Command::new("net")
        .args(["localgroup", group])
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

#[cfg(not(any(unix, windows)))]
fn in_group(_group: &str) -> bool {
    false
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_empty() {
        assert_eq!(parse("").unwrap(), WhenExpr::BoolLit(true));
        assert_eq!(parse("  ").unwrap(), WhenExpr::BoolLit(true));
    }

    #[test]
    fn test_parse_bool_literals() {
        assert_eq!(parse("true").unwrap(), WhenExpr::BoolLit(true));
        assert_eq!(parse("false").unwrap(), WhenExpr::BoolLit(false));
    }

    #[test]
    fn test_parse_var_ref() {
        assert_eq!(
            parse("is_debian").unwrap(),
            WhenExpr::VarRef("is_debian".to_string())
        );
    }

    #[test]
    fn test_parse_not() {
        let expr = parse("not is_debian").unwrap();
        assert_eq!(
            expr,
            WhenExpr::Not(Box::new(WhenExpr::VarRef("is_debian".to_string())))
        );
    }

    #[test]
    fn test_parse_bang_as_not() {
        // `!` is an alias for `not`
        let expr = parse("!is_debian").unwrap();
        assert_eq!(
            expr,
            WhenExpr::Not(Box::new(WhenExpr::VarRef("is_debian".to_string())))
        );

        // With function call
        let expr = parse("!os_family('debian')").unwrap();
        assert!(matches!(expr, WhenExpr::Not(_)));

        // Compound: `!a and b`
        let expr = parse("!a and b").unwrap();
        assert_eq!(
            expr,
            WhenExpr::And(
                Box::new(WhenExpr::Not(Box::new(WhenExpr::VarRef("a".to_string())))),
                Box::new(WhenExpr::VarRef("b".to_string())),
            )
        );
    }

    #[test]
    fn test_parse_and_or() {
        let expr = parse("a and b or c").unwrap();
        // `and` binds tighter: (a and b) or c
        assert_eq!(
            expr,
            WhenExpr::Or(
                Box::new(WhenExpr::And(
                    Box::new(WhenExpr::VarRef("a".to_string())),
                    Box::new(WhenExpr::VarRef("b".to_string()))
                )),
                Box::new(WhenExpr::VarRef("c".to_string()))
            )
        );
    }

    #[test]
    fn test_parse_func_call() {
        let expr = parse("file_exists('/etc/app')").unwrap();
        assert_eq!(
            expr,
            WhenExpr::FuncCall {
                name: "file_exists".to_string(),
                args: vec!["/etc/app".to_string()]
            }
        );
    }

    #[test]
    fn test_parse_func_call_multi_args() {
        let expr = parse("hour_range('9', '17')").unwrap();
        assert_eq!(
            expr,
            WhenExpr::FuncCall {
                name: "hour_range".to_string(),
                args: vec!["9".to_string(), "17".to_string()]
            }
        );
    }

    #[test]
    fn test_parse_parens() {
        let expr = parse("a and (b or c)").unwrap();
        assert_eq!(
            expr,
            WhenExpr::And(
                Box::new(WhenExpr::VarRef("a".to_string())),
                Box::new(WhenExpr::Or(
                    Box::new(WhenExpr::VarRef("b".to_string())),
                    Box::new(WhenExpr::VarRef("c".to_string()))
                ))
            )
        );
    }

    #[test]
    fn test_eval_simple() {
        let expr = parse("is_debian and not is_centos").unwrap();
        let result = eval(&expr, &|name| match name {
            "is_debian" => true,
            "is_centos" => false,
            _ => false,
        })
        .unwrap();
        assert!(result);
    }

    #[test]
    fn test_eval_func_call_error() {
        let expr = parse("file_exists('/etc/app')").unwrap();
        assert!(eval(&expr, &|_| false).is_err());
    }

    #[test]
    fn test_eval_with_funcs() {
        let expr = parse("is_ok and file_exists('/etc/app')").unwrap();
        let result = eval_with_funcs(
            &expr,
            &|name| name == "is_ok",
            Some(&|name, args| {
                Ok(name == "file_exists" && args.first().map(|s| s.as_str()) == Some("/etc/app"))
            }),
        )
        .unwrap();
        assert!(result);
    }

    #[test]
    fn test_contains_func_calls() {
        assert!(!contains_func_calls(&parse("a and b").unwrap()));
        assert!(contains_func_calls(
            &parse("a and file_exists('/x')").unwrap()
        ));
        assert!(contains_func_calls(
            &parse("not file_exists('/x')").unwrap()
        ));
    }

    #[test]
    fn test_substitute_vars() {
        let expr = parse("enabled and file_exists('/x')").unwrap();
        let mut vars = serde_json::Map::new();
        vars.insert("enabled".to_string(), serde_json::Value::String("true".to_string()));
        let sub = substitute_vars(&expr, &vars);
        assert_eq!(
            sub,
            WhenExpr::And(
                Box::new(WhenExpr::BoolLit(true)),
                Box::new(WhenExpr::FuncCall {
                    name: "file_exists".to_string(),
                    args: vec!["/x".to_string()]
                })
            )
        );
    }

    #[test]
    fn test_tristate_eval() {
        // false and func_call → false (short-circuit)
        let expr = WhenExpr::And(
            Box::new(WhenExpr::BoolLit(false)),
            Box::new(WhenExpr::FuncCall {
                name: "f".to_string(),
                args: vec![],
            }),
        );
        assert_eq!(tristate_eval(&expr), Some(false));

        // true or func_call → true (short-circuit)
        let expr = WhenExpr::Or(
            Box::new(WhenExpr::BoolLit(true)),
            Box::new(WhenExpr::FuncCall {
                name: "f".to_string(),
                args: vec![],
            }),
        );
        assert_eq!(tristate_eval(&expr), Some(true));

        // true and func_call → unknown
        let expr = WhenExpr::And(
            Box::new(WhenExpr::BoolLit(true)),
            Box::new(WhenExpr::FuncCall {
                name: "f".to_string(),
                args: vec![],
            }),
        );
        assert_eq!(tristate_eval(&expr), None);
    }

    #[test]
    fn test_expr_string() {
        let cases = vec![
            ("true", "true"),
            ("false", "false"),
            ("is_debian", "is_debian"),
            ("not is_debian", "not is_debian"),
            ("a and b", "a and b"),
            ("a or b", "a or b"),
            ("file_exists('/etc/app')", "file_exists('/etc/app')"),
            ("hour_range('9', '17')", "hour_range('9', '17')"),
        ];
        for (input, expected) in cases {
            let expr = parse(input).unwrap();
            assert_eq!(expr_string(&expr), expected, "input: {}", input);
        }
    }

    #[test]
    fn test_when_var_is_true() {
        let mut vars = serde_json::Map::new();
        vars.insert("enabled".to_string(), serde_json::Value::String("true".to_string()));
        vars.insert("disabled".to_string(), serde_json::Value::String("false".to_string()));
        vars.insert("num".to_string(), serde_json::json!(42));

        assert!(when_var_is_true("enabled", &vars));
        assert!(!when_var_is_true("disabled", &vars));
        assert!(!when_var_is_true("num", &vars));
        assert!(!when_var_is_true("missing", &vars));
    }

    // ── Builtin function tests ──────────────────────────────────────

    fn empty_traits() -> Value {
        serde_json::json!({})
    }

    #[test]
    fn test_builtin_file_exists() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("exists.txt");
        std::fs::write(&path, "hi").unwrap();

        let t = empty_traits();
        assert!(evaluate_builtin("file_exists", &[path.to_str().unwrap().to_string()], &t).unwrap());
        assert!(!evaluate_builtin("file_exists", &["/tmp/nonexistent_builtin_test_1234".to_string()], &t).unwrap());
        // A directory should return false for file_exists.
        assert!(!evaluate_builtin("file_exists", &[dir.path().to_str().unwrap().to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_dir_exists() {
        let dir = tempfile::tempdir().unwrap();
        let t = empty_traits();
        assert!(evaluate_builtin("dir_exists", &[dir.path().to_str().unwrap().to_string()], &t).unwrap());
        assert!(!evaluate_builtin("dir_exists", &["/tmp/nonexistent_builtin_test_dir_1234".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_command_succeeds() {
        let t = empty_traits();
        assert!(evaluate_builtin("command_succeeds", &["true".to_string()], &t).unwrap());
        assert!(!evaluate_builtin("command_succeeds", &["false".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_process_running() {
        let t = empty_traits();
        // PID 1 always has a comm file on Linux.
        let comm = std::fs::read_to_string("/proc/1/comm")
            .unwrap_or_default()
            .trim()
            .to_string();
        if !comm.is_empty() {
            assert!(evaluate_builtin("process_running", &[comm], &t).unwrap());
        }
        assert!(!evaluate_builtin("process_running", &["zzz_nonexistent_process_12345".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_hour_range() {
        // Test the helper directly for deterministic results.
        assert!(hour_in_range(10, 9, 17));
        assert!(!hour_in_range(8, 9, 17));
        assert!(!hour_in_range(17, 9, 17)); // end is exclusive
        // Midnight wrapping.
        assert!(hour_in_range(23, 22, 4));
        assert!(hour_in_range(0, 22, 4));
        assert!(hour_in_range(3, 22, 4));
        assert!(!hour_in_range(4, 22, 4));
        assert!(!hour_in_range(12, 22, 4));

        // Integration: current hour should be in a range that spans 0..24.
        let t = empty_traits();
        assert!(evaluate_builtin("hour_range", &["0".to_string(), "24".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_minute_range() {
        // Test the helper directly for deterministic results.
        assert!(minute_in_range(15, 0, 30));
        assert!(!minute_in_range(35, 0, 30));
        assert!(!minute_in_range(30, 0, 30)); // end is exclusive
        // Wrapping.
        assert!(minute_in_range(50, 45, 15));
        assert!(minute_in_range(0, 45, 15));
        assert!(minute_in_range(14, 45, 15));
        assert!(!minute_in_range(15, 45, 15));
        assert!(!minute_in_range(30, 45, 15));

        // Integration: current minute should be in 0..60.
        let t = empty_traits();
        assert!(evaluate_builtin("minute_range", &["0".to_string(), "60".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_day_of_week() {
        let t = empty_traits();
        // We can't predict the current day, but we can verify it doesn't error.
        let result = evaluate_builtin("day_of_week", &["monday".to_string()], &t);
        assert!(result.is_ok());
    }

    #[test]
    fn test_builtin_day_of_month() {
        let t = empty_traits();
        // Current day should match itself.
        let today = local_day_of_month();
        assert!(evaluate_builtin("day_of_month", &[today.to_string()], &t).unwrap());
        // Day 0 never valid.
        assert!(!evaluate_builtin("day_of_month", &["0".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_flag_set() {
        let t = empty_traits();
        // Non-existent flag should be false.
        assert!(!evaluate_builtin("flag_set", &["nonexistent_test_flag_xyz".to_string()], &t).unwrap());
        // We can't easily test true without creating /var/lib/vigo/flags/... which requires root.
    }

    #[test]
    fn test_builtin_os_family() {
        let traits = serde_json::json!({"os": {"family": "debian", "distro": "ubuntu", "arch": "amd64", "version": "22.04"}});
        assert!(evaluate_builtin("os_family", &["debian".to_string()], &traits).unwrap());
        assert!(evaluate_builtin("os_family", &["Debian".to_string()], &traits).unwrap()); // case-insensitive
        assert!(!evaluate_builtin("os_family", &["redhat".to_string()], &traits).unwrap());
    }

    #[test]
    fn test_builtin_distro() {
        let traits = serde_json::json!({"os": {"family": "debian", "distro": "ubuntu"}});
        assert!(evaluate_builtin("distro", &["ubuntu".to_string()], &traits).unwrap());
        assert!(evaluate_builtin("distro", &["Ubuntu".to_string()], &traits).unwrap());
        assert!(!evaluate_builtin("distro", &["centos".to_string()], &traits).unwrap());
    }

    #[test]
    fn test_builtin_arch() {
        let traits = serde_json::json!({"os": {"arch": "amd64"}});
        assert!(evaluate_builtin("arch", &["amd64".to_string()], &traits).unwrap());
        assert!(!evaluate_builtin("arch", &["arm64".to_string()], &traits).unwrap());
    }

    #[test]
    fn test_builtin_version_ge() {
        let traits = serde_json::json!({"os": {"version": "22.04"}});
        assert!(evaluate_builtin("version_ge", &["22.04".to_string()], &traits).unwrap()); // equal
        assert!(evaluate_builtin("version_ge", &["20.04".to_string()], &traits).unwrap()); // greater
        assert!(!evaluate_builtin("version_ge", &["24.04".to_string()], &traits).unwrap()); // less
    }

    #[test]
    fn test_builtin_in_group() {
        let t = empty_traits();
        // "root" group always exists on Linux.
        assert!(evaluate_builtin("in_group", &["root".to_string()], &t).unwrap());
        assert!(!evaluate_builtin("in_group", &["nonexistent_group_xyz_12345".to_string()], &t).unwrap());
    }

    #[test]
    fn test_builtin_port_listening() {
        let traits = serde_json::json!({"ports": {"listening": [22, 80, 443]}});
        assert!(evaluate_builtin("port_listening", &["80".to_string()], &traits).unwrap());
        assert!(evaluate_builtin("port_listening", &["22".to_string()], &traits).unwrap());
        assert!(!evaluate_builtin("port_listening", &["8080".to_string()], &traits).unwrap());

        // Missing ports trait → false.
        let empty = empty_traits();
        assert!(!evaluate_builtin("port_listening", &["80".to_string()], &empty).unwrap());

        // Invalid port number → error.
        assert!(evaluate_builtin("port_listening", &["not_a_port".to_string()], &traits).is_err());
    }

    #[test]
    fn test_builtin_unknown_function() {
        let t = empty_traits();
        assert!(evaluate_builtin("bogus_func", &[], &t).is_err());
    }

    #[test]
    fn test_eval_with_builtin_and_vars() {
        // Combined: var already resolved to true + file_exists on a real temp file.
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("test_file");
        std::fs::write(&path, "content").unwrap();

        let expr_str = format!("true and file_exists('{}')", path.display());
        let expr = parse(&expr_str).unwrap();
        let traits = empty_traits();
        let result = eval_with_funcs(
            &expr,
            &|_| false,
            Some(&|name, args| evaluate_builtin(name, args, &traits)),
        )
        .unwrap();
        assert!(result);

        // false and file_exists -> short-circuit false.
        let expr2 = parse(&format!("false and file_exists('{}')", path.display())).unwrap();
        assert!(!eval_with_funcs(
            &expr2,
            &|_| false,
            Some(&|name, args| evaluate_builtin(name, args, &traits)),
        )
        .unwrap());
    }

    #[test]
    fn test_version_ge_segments() {
        assert!(version_ge("1", "1.0"));    // 1 == 1.0
        assert!(version_ge("1.0", "1"));    // 1.0 == 1
        assert!(version_ge("2", "1.9.9"));  // 2 > 1.9.9
        assert!(!version_ge("1.9.9", "2")); // 1.9.9 < 2
        assert!(version_ge("22.10", "22.04"));
        assert!(!version_ge("22.04", "22.10"));
        assert!(version_ge("1.0.0", "1.0.0")); // exactly equal
    }

    /// has_command / has_service / has_package must never hand their argument
    /// to a shell: `has_command('true; touch X')` used to run the touch.
    #[cfg(unix)]
    #[test]
    fn has_builtins_do_not_shell_inject() {
        let dir = tempfile::tempdir().unwrap();
        let pwned = dir.path().join("pwned");
        let t = Value::Null;
        for f in ["has_command", "has_service", "has_package"] {
            let arg = format!("x; touch {}", pwned.display());
            let _ = evaluate_builtin(f, &[arg], &t);
            assert!(!pwned.exists(), "{f} executed an injected shell command");
        }
        assert!(evaluate_builtin("has_command", &["sh".to_string()], &t).unwrap());
        assert!(!evaluate_builtin("has_command", &["no-such-cmd-vigo-t4c".to_string()], &t).unwrap());
    }
}
