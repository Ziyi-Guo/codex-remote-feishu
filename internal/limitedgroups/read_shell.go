package limitedgroups

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// Parse shell syntax without executing it. Only literal arguments are evaluated;
// there are no environment, command, process, arithmetic or file expansions.
func parseReadShell(command string) (*syntax.File, bool) {
	if len(command) == 0 || len(command) > 32768 || !utf8.ValidString(command) {
		return nil, false
	}
	for _, r := range command {
		if unicode.IsControl(r) {
			return nil, false
		}
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(true)).Parse(strings.NewReader(command), "")
	if err != nil || len(f.Stmts) == 0 || len(f.Stmts) > 16 || len(f.Last) != 0 || f.Stmts[len(f.Stmts)-1].Semicolon.IsValid() {
		return nil, false
	}
	return f, true
}

func literalWordParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if !quoted && strings.ContainsAny(p.Value, "\\*?[]~{}") {
				return false
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if p.Dollar || !literalWordParts(p.Parts, true) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func literalCall(stmt *syntax.Stmt) ([]string, bool) {
	if stmt == nil || stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) != 0 || len(stmt.Comments) != 0 {
		return nil, false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(call.Args) == 0 {
		return nil, false
	}
	argv := make([]string, 0, len(call.Args))
	for _, w := range call.Args {
		if !literalWordParts(w.Parts, false) {
			return nil, false
		}
		value, err := expand.Literal(nil, w)
		if err != nil {
			return nil, false
		}
		argv = append(argv, value)
	}
	return argv, true
}

func literalArgv(command string) ([]string, bool) {
	f, ok := parseReadShell(command)
	if !ok || len(f.Stmts) != 1 {
		return nil, false
	}
	return literalCall(f.Stmts[0])
}

func knownReadProgram(command string, allowShell bool, count *int) bool {
	f, ok := parseReadShell(command)
	if !ok {
		return false
	}
	for _, stmt := range f.Stmts {
		if !knownReadStatement(stmt, allowShell, count, 0) {
			return false
		}
	}
	return true
}

func knownReadStatement(stmt *syntax.Stmt, allowShell bool, count *int, depth int) bool {
	if stmt == nil || depth > 8 || stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) != 0 || len(stmt.Comments) != 0 {
		return false
	}
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		return binary.Op == syntax.AndStmt && knownReadStatement(binary.X, allowShell, count, depth+1) && knownReadStatement(binary.Y, allowShell, count, depth+1)
	}
	argv, ok := literalCall(stmt)
	if !ok {
		return false
	}
	*count++
	if *count > 16 {
		return false
	}
	if allowShell && len(argv) == 3 && oneOf(argv[0], "/bin/sh", "/bin/bash", "/bin/zsh") && oneOf(argv[1], "-c", "-lc") {
		return knownReadProgram(argv[2], false, count)
	}
	return isKnownReadArgv(argv)
}
