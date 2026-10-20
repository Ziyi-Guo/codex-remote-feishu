package limitedgroups

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsKnownReadCommand recognizes a deliberately small, literal subset. False
// leaves the native escalation for owner review; it does not disable the tool.
// This is a surface classifier, not a SQL sandbox or a general shell parser.
func IsKnownReadCommand(command string) bool {
	argv, ok := literalArgv(command)
	if !ok || len(argv) < 4 || argv[0] != "bytedcli" {
		return false
	}
	i := 1
	seen := map[string]bool{}
	for i < len(argv) && strings.HasPrefix(argv[i], "-") {
		flag := argv[i]
		if flag == "-j" {
			flag = "--json"
		}
		if seen[flag] {
			return false
		}
		seen[flag] = true
		switch flag {
		case "--json":
			i++
		case "--site":
			if i+1 >= len(argv) || !oneOf(argv[i+1], "cn", "boe", "i18n", "i18n-bd", "i18n-tt", "us-ttp", "eu-ttp") {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	if i+2 >= len(argv) || argv[i] != "rds" || argv[i+1] != "db" {
		return false
	}
	i += 2
	verb := argv[i]
	i++
	if verb == "table" {
		if i >= len(argv) {
			return false
		}
		verb += " " + argv[i]
		i++
	}
	nargs := 0
	flags := map[string]string{"--region": "region", "-r": "region", "--mode": "mode"}
	switch verb {
	case "list":
		flags["--starred"] = "bool"
	case "search":
		nargs = 1
	case "overview":
		nargs = 1
		flags["--qps"], flags["--no-topo"] = "bool", "bool"
	case "table list":
		nargs = 1
	case "table schema":
		nargs = 2
	case "query":
		nargs = 2
	default:
		return false
	}
	if verb == "list" || verb == "search" || verb == "table list" {
		flags["--page"], flags["--page-size"] = "page", "size"
	}
	if verb == "list" || verb == "search" || verb == "overview" {
		flags["--vregion"] = "name"
	}
	if i+nargs > len(argv) {
		return false
	}
	for n := 0; n < nargs; n++ {
		if verb == "query" && n == 1 {
			if !conservativeReadSQL(argv[i+n]) {
				return false
			}
		} else if !literalName(argv[i+n]) {
			return false
		}
	}
	i += nargs
	seen = map[string]bool{}
	for i < len(argv) {
		flag, value, assigned := strings.Cut(argv[i], "=")
		kind, known := flags[flag]
		if flag == "-r" {
			flag = "--region"
		}
		if !known || seen[flag] {
			return false
		}
		seen[flag] = true
		i++
		if kind == "bool" {
			if assigned {
				return false
			}
			continue
		}
		if !assigned {
			if i >= len(argv) {
				return false
			}
			value = argv[i]
			i++
		}
		switch kind {
		case "region", "name":
			if !approvedRDSRegion(value) {
				return false
			}
		case "mode":
			if !oneOf(value, "auto", "legacy", "dbw") {
				return false
			}
		case "page":
			if !boundedNumber(value, 0, 100000) {
				return false
			}
		case "size":
			if !boundedNumber(value, 1, 1000) {
				return false
			}
		}
	}
	return true
}

// Only plain words and single/double quoted strings are admitted. Rejecting all
// escapes and expansion markers, even when quoted, avoids shell-mode ambiguity.
func literalArgv(command string) ([]string, bool) {
	if len(command) > 32768 || !utf8.ValidString(command) {
		return nil, false
	}
	var argv []string
	var word strings.Builder
	var quote rune
	started := false
	for _, r := range command {
		if unicode.IsControl(r) || strings.ContainsRune("$`\\", r) {
			return nil, false
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote, started = r, true
			continue
		}
		if r == ' ' {
			if started {
				argv = append(argv, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		if unicode.IsSpace(r) || strings.ContainsRune(";&|<>(){}[]*?~#", r) {
			return nil, false
		}
		started = true
		word.WriteRune(r)
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		argv = append(argv, word.String())
	}
	return argv, len(argv) > 0
}

func literalName(value string) bool {
	if value == "" || len(value) > 512 || value[0] == '-' {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
			return false
		}
	}
	return true
}

// Enumerated from the installed RDS read-command metadata; additions require review.
func approvedRDSRegion(value string) bool {
	return oneOf(value, "cn", "sinf", "huabei2", "alisg", "maliva", "mycis", "boe", "US-EE", "Asia-SaaS", "Singapore-SaaS", "Singapore-Common", "Asia-CIS", "Asia-SouthEastBD", "EU-TTP", "EU-TTP2", "EU-Compliance", "EU-Compliance2", "ChinaSinf-North")
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func boundedNumber(value string, minimum, maximum int) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= minimum && n <= maximum
}

type sqlToken struct{ kind, text string }
type sqlReader struct {
	tokens []sqlToken
	index  int
}

func conservativeReadSQL(sql string) bool {
	if len(sql) > 16384 || strings.ContainsAny(sql, ";#\\") || strings.Contains(sql, "--") || strings.Contains(sql, "/*") || strings.Contains(sql, "*/") {
		return false
	}
	tokens, ok := readSQLTokens(sql)
	if !ok || len(tokens) == 0 {
		return false
	}
	p := &sqlReader{tokens: tokens}
	switch {
	case p.take("SELECT"):
		if !p.selectQuery() {
			return false
		}
	case p.take("SHOW"):
		switch {
		case p.take("TABLES"):
			if p.take("FROM") && !p.identifier() {
				return false
			}
			if p.take("LIKE") && !p.stringLiteral() {
				return false
			}
		case p.take("CREATE"):
			if !p.take("TABLE") || !p.identifier() {
				return false
			}
		case p.take("COLUMNS"), p.take("INDEX"):
			if !p.take("FROM") || !p.identifier() {
				return false
			}
		case p.take("DATABASES"):
		default:
			return false
		}
	case p.take("DESCRIBE"):
		if !p.identifier() {
			return false
		}
	default:
		return false
	}
	return p.index == len(tokens)
}

func readSQLTokens(sql string) ([]sqlToken, bool) {
	var tokens []sqlToken
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == ' ' {
			i++
			continue
		}
		if c == '\'' {
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == '\'' {
					i++
					if i < len(sql) && sql[i] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false
			}
			tokens = append(tokens, sqlToken{"string", ""})
			continue
		}
		if c >= '0' && c <= '9' {
			start := i
			for i < len(sql) && sql[i] >= '0' && sql[i] <= '9' {
				i++
			}
			tokens = append(tokens, sqlToken{"number", sql[start:i]})
			continue
		}
		if sqlLetter(c) {
			start := i
			for i < len(sql) && (sqlLetter(sql[i]) || sql[i] >= '0' && sql[i] <= '9') {
				i++
			}
			text := strings.ToUpper(sql[start:i])
			if oneOf(text, "INSERT", "UPDATE", "DELETE", "REPLACE", "DROP", "ALTER", "TRUNCATE", "INTO", "OUTFILE", "DUMPFILE", "FOR", "LOCK", "SHARE", "CALL", "UNION", "INTERSECT", "EXCEPT", "WITH", "PROCEDURE", "HANDLER", "DO", "SET", "USE", "KILL", "GRANT", "REVOKE", "ANALYZE", "OPTIMIZE", "REPAIR", "FLUSH", "RESET", "PREPARE", "EXECUTE", "DEALLOCATE") {
				return nil, false
			}
			tokens = append(tokens, sqlToken{"word", text})
			continue
		}
		if !strings.ContainsRune("(),.*=<>!", rune(c)) {
			return nil, false
		}
		text := string(c)
		i++
		if i < len(sql) && (c == '<' && sql[i] == '>' || (c == '<' || c == '>' || c == '!') && sql[i] == '=') {
			text += string(sql[i])
			i++
		}
		tokens = append(tokens, sqlToken{"symbol", text})
	}
	return tokens, true
}

func sqlLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
func (p *sqlReader) take(text string) bool {
	if p.index >= len(p.tokens) || p.tokens[p.index].text != text {
		return false
	}
	p.index++
	return true
}
func (p *sqlReader) stringLiteral() bool {
	if p.index >= len(p.tokens) || p.tokens[p.index].kind != "string" {
		return false
	}
	p.index++
	return true
}
func (p *sqlReader) identifier() bool {
	if p.index >= len(p.tokens) || p.tokens[p.index].kind != "word" || oneOf(p.tokens[p.index].text, "SELECT", "FROM", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "AS", "AND", "OR", "BY", "ASC", "DESC") {
		return false
	}
	p.index++
	if p.take(".") {
		if p.index >= len(p.tokens) || p.tokens[p.index].kind != "word" {
			return false
		}
		p.index++
	}
	return true
}
func (p *sqlReader) expression(depth int) bool {
	if depth > 12 || p.index >= len(p.tokens) {
		return false
	}
	token := p.tokens[p.index]
	if token.kind == "number" || token.kind == "string" {
		p.index++
		return true
	}
	start := p.index
	if !p.identifier() {
		return false
	}
	if !p.take("(") {
		return true
	}
	if p.index != start+2 || !oneOf(token.text, "COUNT", "SUM", "AVG", "MIN", "MAX", "LENGTH", "LOWER", "UPPER", "TRIM", "CONCAT", "COALESCE", "IFNULL", "SUBSTRING", "SUBSTR", "ROUND", "ABS") {
		return false
	}
	if token.text == "COUNT" && p.take("*") {
		return p.take(")")
	}
	if !p.expression(depth + 1) {
		return false
	}
	for p.take(",") {
		if !p.expression(depth + 1) {
			return false
		}
	}
	return p.take(")")
}
func (p *sqlReader) predicate(depth int) bool {
	if depth > 12 {
		return false
	}
	if p.take("(") {
		if !p.conditions(depth+1) || !p.take(")") {
			return false
		}
		return true
	}
	if !p.expression(0) {
		return false
	}
	if p.take("IS") {
		p.take("NOT")
		return p.take("NULL")
	}
	if p.take("IN") {
		if !p.take("(") || !p.expression(0) {
			return false
		}
		for p.take(",") {
			if !p.expression(0) {
				return false
			}
		}
		return p.take(")")
	}
	if p.take("LIKE") || p.take("=") || p.take("!=") || p.take("<>") || p.take("<") || p.take(">") || p.take("<=") || p.take(">=") {
		return p.expression(0)
	}
	return true
}
func (p *sqlReader) conditions(depth int) bool {
	if !p.predicate(depth) {
		return false
	}
	for p.take("AND") || p.take("OR") {
		if !p.predicate(depth) {
			return false
		}
	}
	return true
}
func (p *sqlReader) expressionList() bool {
	if !p.expression(0) {
		return false
	}
	for p.take(",") {
		if !p.expression(0) {
			return false
		}
	}
	return true
}
func (p *sqlReader) selectQuery() bool {
	p.take("DISTINCT")
	if !p.take("*") && !p.expressionList() {
		return false
	}
	if p.take("FROM") && !p.identifier() {
		return false
	}
	if p.take("WHERE") && !p.conditions(0) {
		return false
	}
	if p.take("GROUP") && (!p.take("BY") || !p.expressionList()) {
		return false
	}
	if p.take("HAVING") && !p.conditions(0) {
		return false
	}
	if p.take("ORDER") {
		if !p.take("BY") {
			return false
		}
		for {
			if !p.expression(0) {
				return false
			}
			if !p.take("ASC") {
				p.take("DESC")
			}
			if !p.take(",") {
				break
			}
		}
	}
	if !p.take("LIMIT") || p.index >= len(p.tokens) || !boundedNumber(p.tokens[p.index].text, 1, 1000) {
		return false
	}
	p.index++
	if p.take("OFFSET") {
		if p.index >= len(p.tokens) || !boundedNumber(p.tokens[p.index].text, 0, 100000) {
			return false
		}
		p.index++
	}
	return true
}
