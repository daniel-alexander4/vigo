// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! When expression parser, evaluator, and helpers.
//! Port of server/conditionals/when.go (501 lines).
//!
//! Grammar:
//!   or_expr  → and_expr ('or' and_expr)*
//!   and_expr → unary ('and' unary)*
//!   unary    → 'not' unary | primary
//!   primary  → 'true' | 'false' | ident | ident '(' args ')' | '(' or_expr ')'

use anyhow::{bail, Result};

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
    funcs: Option<&dyn Fn(&str, &[String]) -> Result<bool>>,
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
        vars.insert("num".to_string(), serde_json::json!(42).into());

        assert!(when_var_is_true("enabled", &vars));
        assert!(!when_var_is_true("disabled", &vars));
        assert!(!when_var_is_true("num", &vars));
        assert!(!when_var_is_true("missing", &vars));
    }
}
