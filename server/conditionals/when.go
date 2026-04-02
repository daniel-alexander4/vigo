// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package conditionals

import (
	"fmt"
	"strings"
	"unicode"
)

// -- AST node types for when expressions --

// WhenExpr is the interface for all when expression AST nodes.
type WhenExpr interface {
	whenExprNode()
}

// BoolLitExpr represents a boolean literal (true/false).
type BoolLitExpr struct{ Value bool }

// VarRefExpr represents a variable reference (identifier).
type VarRefExpr struct{ Name string }

// NotWhenExpr represents a logical NOT.
type NotWhenExpr struct{ Operand WhenExpr }

// AndWhenExpr represents a logical AND.
type AndWhenExpr struct{ Left, Right WhenExpr }

// OrWhenExpr represents a logical OR.
type OrWhenExpr struct{ Left, Right WhenExpr }

// FuncCallExpr represents a builtin function call (e.g., file_exists('/path')).
type FuncCallExpr struct {
	Name string
	Args []string
}

func (BoolLitExpr) whenExprNode()  {}
func (VarRefExpr) whenExprNode()   {}
func (NotWhenExpr) whenExprNode()  {}
func (AndWhenExpr) whenExprNode()  {}
func (OrWhenExpr) whenExprNode()   {}
func (FuncCallExpr) whenExprNode() {}

// -- Lexer --

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokTrue
	tokFalse
	tokAnd
	tokOr
	tokNot
	tokLParen
	tokRParen
	tokString // single-quoted string
	tokComma
)

type token struct {
	kind tokenKind
	val  string
}

type lexer struct {
	input  string
	pos    int
	tokens []token
}

func lex(input string) ([]token, error) {
	l := &lexer{input: input}
	for {
		l.skipWhitespace()
		if l.pos >= len(l.input) {
			l.tokens = append(l.tokens, token{kind: tokEOF})
			return l.tokens, nil
		}

		ch := l.input[l.pos]
		switch {
		case ch == '(':
			l.tokens = append(l.tokens, token{kind: tokLParen, val: "("})
			l.pos++
		case ch == ')':
			l.tokens = append(l.tokens, token{kind: tokRParen, val: ")"})
			l.pos++
		case ch == ',':
			l.tokens = append(l.tokens, token{kind: tokComma, val: ","})
			l.pos++
		case ch == '\'':
			str, err := l.lexString()
			if err != nil {
				return nil, err
			}
			l.tokens = append(l.tokens, token{kind: tokString, val: str})
		case unicode.IsLetter(rune(ch)) || ch == '_':
			word := l.lexWord()
			switch word {
			case "true":
				l.tokens = append(l.tokens, token{kind: tokTrue, val: word})
			case "false":
				l.tokens = append(l.tokens, token{kind: tokFalse, val: word})
			case "and":
				l.tokens = append(l.tokens, token{kind: tokAnd, val: word})
			case "or":
				l.tokens = append(l.tokens, token{kind: tokOr, val: word})
			case "not":
				l.tokens = append(l.tokens, token{kind: tokNot, val: word})
			default:
				l.tokens = append(l.tokens, token{kind: tokIdent, val: word})
			}
		default:
			return nil, fmt.Errorf("unexpected character %q at position %d", ch, l.pos)
		}
	}
}

func (l *lexer) skipWhitespace() {
	for l.pos < len(l.input) && unicode.IsSpace(rune(l.input[l.pos])) {
		l.pos++
	}
}

func (l *lexer) lexWord() string {
	start := l.pos
	for l.pos < len(l.input) {
		ch := rune(l.input[l.pos])
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' {
			l.pos++
		} else {
			break
		}
	}
	return l.input[start:l.pos]
}

func (l *lexer) lexString() (string, error) {
	l.pos++ // skip opening quote
	start := l.pos
	for l.pos < len(l.input) {
		if l.input[l.pos] == '\'' {
			str := l.input[start:l.pos]
			l.pos++ // skip closing quote
			return str, nil
		}
		if l.input[l.pos] == '\\' && l.pos+1 < len(l.input) {
			l.pos += 2
			continue
		}
		l.pos++
	}
	return "", fmt.Errorf("unterminated string literal starting at position %d", start-1)
}

// -- Parser --

type whenParser struct {
	tokens []token
	pos    int
}

// ParseWhenExpr parses a when expression string into an AST.
// An empty string returns BoolLitExpr{true} (always run).
func ParseWhenExpr(input string) (WhenExpr, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return &BoolLitExpr{Value: true}, nil
	}

	tokens, err := lex(input)
	if err != nil {
		return nil, fmt.Errorf("lexing when expression: %w", err)
	}

	p := &whenParser{tokens: tokens}
	expr, err := p.parseOrExpr()
	if err != nil {
		return nil, err
	}

	if p.current().kind != tokEOF {
		return nil, fmt.Errorf("unexpected token %q at end of expression", p.current().val)
	}

	return expr, nil
}

func (p *whenParser) current() token {
	if p.pos >= len(p.tokens) {
		return token{kind: tokEOF}
	}
	return p.tokens[p.pos]
}

func (p *whenParser) advance() token {
	t := p.current()
	p.pos++
	return t
}

func (p *whenParser) parseOrExpr() (WhenExpr, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}
	for p.current().kind == tokOr {
		p.advance()
		right, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		left = &OrWhenExpr{Left: left, Right: right}
	}
	return left, nil
}

func (p *whenParser) parseAndExpr() (WhenExpr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.current().kind == tokAnd {
		p.advance()
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &AndWhenExpr{Left: left, Right: right}
	}
	return left, nil
}

func (p *whenParser) parseUnary() (WhenExpr, error) {
	if p.current().kind == tokNot {
		p.advance()
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &NotWhenExpr{Operand: operand}, nil
	}
	return p.parsePrimary()
}

func (p *whenParser) parsePrimary() (WhenExpr, error) {
	switch p.current().kind {
	case tokTrue:
		p.advance()
		return &BoolLitExpr{Value: true}, nil
	case tokFalse:
		p.advance()
		return &BoolLitExpr{Value: false}, nil
	case tokIdent:
		name := p.advance().val
		if p.current().kind == tokLParen {
			return p.parseFuncCall(name)
		}
		return &VarRefExpr{Name: name}, nil
	case tokLParen:
		p.advance()
		expr, err := p.parseOrExpr()
		if err != nil {
			return nil, err
		}
		if p.current().kind != tokRParen {
			return nil, fmt.Errorf("expected ')' but got %q", p.current().val)
		}
		p.advance()
		return expr, nil
	default:
		return nil, fmt.Errorf("unexpected token %q", p.current().val)
	}
}

func (p *whenParser) parseFuncCall(name string) (WhenExpr, error) {
	p.advance() // skip '('
	var args []string
	for p.current().kind != tokRParen {
		if len(args) > 0 {
			if p.current().kind != tokComma {
				return nil, fmt.Errorf("expected ',' between function arguments")
			}
			p.advance()
		}
		if p.current().kind != tokString {
			return nil, fmt.Errorf("function arguments must be single-quoted strings")
		}
		args = append(args, p.advance().val)
	}
	if p.current().kind != tokRParen {
		return nil, fmt.Errorf("expected ')' after function arguments")
	}
	p.advance()
	return &FuncCallExpr{Name: name, Args: args}, nil
}

// -- Evaluator --

// EvalWhenExpr evaluates a when expression using the given variable resolver.
// Returns an error if builtin function calls are encountered.
func EvalWhenExpr(expr WhenExpr, resolve func(string) bool) (bool, error) {
	return EvalWhenExprWithFuncs(expr, resolve, nil)
}

// EvalWhenExprWithFuncs evaluates a when expression using the given variable
// resolver and optional function resolver for builtins.
func EvalWhenExprWithFuncs(expr WhenExpr, resolve func(string) bool, funcs func(string, []string) (bool, error)) (bool, error) {
	switch e := expr.(type) {
	case *BoolLitExpr:
		return e.Value, nil
	case *VarRefExpr:
		return resolve(e.Name), nil
	case *NotWhenExpr:
		val, err := EvalWhenExprWithFuncs(e.Operand, resolve, funcs)
		if err != nil {
			return false, err
		}
		return !val, nil
	case *AndWhenExpr:
		left, err := EvalWhenExprWithFuncs(e.Left, resolve, funcs)
		if err != nil {
			return false, err
		}
		if !left {
			return false, nil
		}
		return EvalWhenExprWithFuncs(e.Right, resolve, funcs)
	case *OrWhenExpr:
		left, err := EvalWhenExprWithFuncs(e.Left, resolve, funcs)
		if err != nil {
			return false, err
		}
		if left {
			return true, nil
		}
		return EvalWhenExprWithFuncs(e.Right, resolve, funcs)
	case *FuncCallExpr:
		if funcs == nil {
			return false, fmt.Errorf("function call %q not supported in this context", e.Name)
		}
		return funcs(e.Name, e.Args)
	default:
		return false, fmt.Errorf("unknown expression type %T", expr)
	}
}

// -- Helpers --

// ContainsFuncCalls returns true if the expression contains any function calls.
func ContainsFuncCalls(expr WhenExpr) bool {
	switch e := expr.(type) {
	case *BoolLitExpr, *VarRefExpr:
		return false
	case *NotWhenExpr:
		return ContainsFuncCalls(e.Operand)
	case *AndWhenExpr:
		return ContainsFuncCalls(e.Left) || ContainsFuncCalls(e.Right)
	case *OrWhenExpr:
		return ContainsFuncCalls(e.Left) || ContainsFuncCalls(e.Right)
	case *FuncCallExpr:
		return true
	default:
		return false
	}
}

// SubstituteVars returns a new expression tree with all VarRefExpr nodes
// replaced by BoolLitExpr based on the vars map. Function calls are unchanged.
func SubstituteVars(expr WhenExpr, vars map[string]any) WhenExpr {
	switch e := expr.(type) {
	case *BoolLitExpr:
		return e
	case *VarRefExpr:
		return &BoolLitExpr{Value: WhenVarIsTrue(e.Name, vars)}
	case *NotWhenExpr:
		return &NotWhenExpr{Operand: SubstituteVars(e.Operand, vars)}
	case *AndWhenExpr:
		return &AndWhenExpr{Left: SubstituteVars(e.Left, vars), Right: SubstituteVars(e.Right, vars)}
	case *OrWhenExpr:
		return &OrWhenExpr{Left: SubstituteVars(e.Left, vars), Right: SubstituteVars(e.Right, vars)}
	case *FuncCallExpr:
		return e
	default:
		return e
	}
}

// WhenVarIsTrue returns true only if vars[name] == "true" (string).
func WhenVarIsTrue(name string, vars map[string]any) bool {
	v, ok := vars[name]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return s == "true"
}

// TristateEval attempts to evaluate an expression where FuncCallExpr nodes
// are treated as "unknown". Returns nil if the result depends on an unknown
// value, or a *bool if the result can be determined through short-circuit logic.
func TristateEval(expr WhenExpr) *bool {
	switch e := expr.(type) {
	case *BoolLitExpr:
		return &e.Value
	case *VarRefExpr:
		return nil // unknown — variable was not substituted
	case *NotWhenExpr:
		inner := TristateEval(e.Operand)
		if inner == nil {
			return nil
		}
		result := !*inner
		return &result
	case *AndWhenExpr:
		left := TristateEval(e.Left)
		if left != nil && !*left {
			f := false
			return &f // false and ? = false
		}
		right := TristateEval(e.Right)
		if right != nil && !*right {
			f := false
			return &f // ? and false = false
		}
		if left != nil && right != nil {
			result := *left && *right
			return &result
		}
		return nil
	case *OrWhenExpr:
		left := TristateEval(e.Left)
		if left != nil && *left {
			t := true
			return &t // true or ? = true
		}
		right := TristateEval(e.Right)
		if right != nil && *right {
			t := true
			return &t // ? or true = true
		}
		if left != nil && right != nil {
			result := *left || *right
			return &result
		}
		return nil
	case *FuncCallExpr:
		return nil // unknown
	default:
		return nil
	}
}

// ExprString returns the string representation of a when expression AST.
func ExprString(expr WhenExpr) string {
	switch e := expr.(type) {
	case *BoolLitExpr:
		if e.Value {
			return "true"
		}
		return "false"
	case *VarRefExpr:
		return e.Name
	case *NotWhenExpr:
		switch e.Operand.(type) {
		case *AndWhenExpr, *OrWhenExpr:
			return "not (" + ExprString(e.Operand) + ")"
		}
		return "not " + ExprString(e.Operand)
	case *AndWhenExpr:
		left := ExprString(e.Left)
		if _, ok := e.Left.(*OrWhenExpr); ok {
			left = "(" + left + ")"
		}
		right := ExprString(e.Right)
		if _, ok := e.Right.(*OrWhenExpr); ok {
			right = "(" + right + ")"
		}
		return left + " and " + right
	case *OrWhenExpr:
		return ExprString(e.Left) + " or " + ExprString(e.Right)
	case *FuncCallExpr:
		var quotedArgs []string
		for _, arg := range e.Args {
			quotedArgs = append(quotedArgs, "'"+arg+"'")
		}
		return e.Name + "(" + strings.Join(quotedArgs, ", ") + ")"
	default:
		return "?"
	}
}
