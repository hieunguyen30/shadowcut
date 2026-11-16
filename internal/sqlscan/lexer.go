// Package sqlscan is a small, dependency-free PostgreSQL lexer and structural
// analyzer. It is intentionally not a full parser (see docs/DECISIONS.md, ADR-005):
// it tokenizes SQL correctly (strings, dollar quotes, comments, quoted
// identifiers) and extracts the structure Shadowcut needs: statement kind,
// referenced tables, select list, ORDER BY items, LIMIT, function calls.
package sqlscan

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokKind is the lexical class of a token.
type TokKind int

const (
	TIdent  TokKind = iota // bare identifier or keyword (Text is lower-cased)
	TQIdent                // "quoted identifier" (Text is the unquoted, case-preserved name)
	TString                // string literal (Text is the literal body)
	TNumber                // numeric literal
	TParam                 // $1, $2 ...
	TOp                    // operator: = < > <= >= <> != || :: ~ etc.
	TPunct                 // ( ) , ; . [ ]
)

// Token is one lexical token with its source position (1-based line/col).
type Token struct {
	Kind  TokKind
	Text  string
	Raw   string
	Line  int
	Col   int
	Depth int // parenthesis depth at which the token appears
}

// Is reports whether t is the bare keyword/identifier kw (case-insensitive).
func (t Token) Is(kw string) bool { return t.Kind == TIdent && t.Text == kw }

// IsPunct reports whether t is the punctuation p.
func (t Token) IsPunct(p string) bool { return t.Kind == TPunct && t.Text == p }

// IsIdent reports whether the token can name a column/table.
func (t Token) IsIdent() bool { return t.Kind == TIdent || t.Kind == TQIdent }

// Lex tokenizes src. It never fails: malformed input yields best-effort tokens.
func Lex(src string) []Token {
	var toks []Token
	line, col := 1, 1
	depth := 0
	i := 0
	advance := func(n int) {
		for k := 0; k < n && i < len(src); k++ {
			if src[i] == '\n' {
				line++
				col = 1
			} else {
				col++
			}
			i++
		}
	}
	emit := func(kind TokKind, text, raw string, l, c int) {
		toks = append(toks, Token{Kind: kind, Text: text, Raw: raw, Line: l, Col: c, Depth: depth})
	}
	for i < len(src) {
		ch := src[i]
		l, c := line, col
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f':
			advance(1)
		case ch == '-' && i+1 < len(src) && src[i+1] == '-':
			for i < len(src) && src[i] != '\n' {
				advance(1)
			}
		case ch == '/' && i+1 < len(src) && src[i+1] == '*':
			nest := 0
			for i < len(src) {
				if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
					nest++
					advance(2)
					continue
				}
				if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
					nest--
					advance(2)
					if nest == 0 {
						break
					}
					continue
				}
				advance(1)
			}
		case ch == '\'' || ((ch == 'E' || ch == 'e') && i+1 < len(src) && src[i+1] == '\''):
			start := i
			escapes := ch != '\''
			if escapes {
				advance(1)
			}
			advance(1) // opening quote
			var b strings.Builder
			for i < len(src) {
				if escapes && src[i] == '\\' && i+1 < len(src) {
					b.WriteByte(src[i+1])
					advance(2)
					continue
				}
				if src[i] == '\'' {
					if i+1 < len(src) && src[i+1] == '\'' {
						b.WriteByte('\'')
						advance(2)
						continue
					}
					advance(1)
					break
				}
				b.WriteByte(src[i])
				advance(1)
			}
			emit(TString, b.String(), src[start:i], l, c)
		case ch == '$' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			start := i
			advance(1)
			for i < len(src) && src[i] >= '0' && src[i] <= '9' {
				advance(1)
			}
			emit(TParam, src[start:i], src[start:i], l, c)
		case ch == '$':
			// dollar-quoted string: $tag$ ... $tag$
			end := strings.IndexByte(src[i+1:], '$')
			tag := ""
			if end >= 0 {
				tag = src[i : i+1+end+1]
			}
			if tag == "" || !validDollarTag(tag) {
				emit(TOp, "$", "$", l, c)
				advance(1)
				continue
			}
			start := i
			advance(len(tag))
			bodyStart := i
			closeIdx := strings.Index(src[i:], tag)
			if closeIdx < 0 {
				advance(len(src) - i)
				emit(TString, src[bodyStart:], src[start:], l, c)
				continue
			}
			body := src[i : i+closeIdx]
			advance(closeIdx + len(tag))
			emit(TString, body, src[start:i], l, c)
		case ch == '"':
			start := i
			advance(1)
			var b strings.Builder
			for i < len(src) {
				if src[i] == '"' {
					if i+1 < len(src) && src[i+1] == '"' {
						b.WriteByte('"')
						advance(2)
						continue
					}
					advance(1)
					break
				}
				b.WriteByte(src[i])
				advance(1)
			}
			emit(TQIdent, b.String(), src[start:i], l, c)
		case ch >= '0' && ch <= '9' || (ch == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			start := i
			for i < len(src) && (src[i] >= '0' && src[i] <= '9' || src[i] == '.' || src[i] == '_') {
				if src[i] == '.' && i+1 < len(src) && src[i+1] == '.' {
					break
				}
				advance(1)
			}
			if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
				j := i + 1
				if j < len(src) && (src[j] == '+' || src[j] == '-') {
					j++
				}
				if j < len(src) && src[j] >= '0' && src[j] <= '9' {
					advance(j - i)
					for i < len(src) && src[i] >= '0' && src[i] <= '9' {
						advance(1)
					}
				}
			}
			emit(TNumber, src[start:i], src[start:i], l, c)
		case isIdentStart(src, i):
			start := i
			for i < len(src) && isIdentPart(src, i) {
				_, sz := utf8.DecodeRuneInString(src[i:])
				advance(sz)
			}
			raw := src[start:i]
			emit(TIdent, strings.ToLower(raw), raw, l, c)
		case ch == '(':
			emit(TPunct, "(", "(", l, c)
			depth++
			advance(1)
		case ch == ')':
			if depth > 0 {
				depth--
			}
			emit(TPunct, ")", ")", l, c)
			advance(1)
		case strings.IndexByte(",;.[]", ch) >= 0:
			emit(TPunct, string(ch), string(ch), l, c)
			advance(1)
		default:
			start := i
			for i < len(src) && strings.IndexByte("+-*/<>=~!@#%^&|`?:", src[i]) >= 0 {
				// stop before a comment start inside an operator run
				if (src[i] == '-' && i+1 < len(src) && src[i+1] == '-') || (src[i] == '/' && i+1 < len(src) && src[i+1] == '*') {
					if i > start {
						break
					}
				}
				advance(1)
			}
			if i == start {
				advance(1)
			}
			op := src[start:i]
			emit(TOp, op, op, l, c)
		}
	}
	return toks
}

func validDollarTag(tag string) bool {
	if len(tag) < 2 || tag[0] != '$' || tag[len(tag)-1] != '$' {
		return false
	}
	for _, r := range tag[1 : len(tag)-1] {
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func isIdentStart(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r == '_' || unicode.IsLetter(r)
}

func isIdentPart(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Statement is one SQL statement with its tokens and source location.
type Statement struct {
	Text   string
	Tokens []Token
	Line   int               // 1-based line of the first token in the source file
	Name   string            // from a "-- name: x" annotation, if any
	Tags   map[string]string // other "-- key: value" annotations preceding the statement
}

// Split splits a SQL script into statements on top-level semicolons, keeping
// "-- key: value" annotations that immediately precede each statement.
func Split(src string) []Statement {
	toks := Lex(src)
	lines := strings.Split(src, "\n")
	var out []Statement
	var cur []Token
	flush := func(endLine int) {
		if len(cur) == 0 {
			return
		}
		first, last := cur[0], cur[len(cur)-1]
		text := sliceText(lines, first.Line, first.Col, last.Line, last.Col+len(last.Raw))
		st := Statement{Text: strings.TrimSpace(text), Tokens: cur, Line: first.Line, Tags: map[string]string{}}
		// annotations: walk upward over contiguous comment lines
		for ln := first.Line - 1; ln >= 1; ln-- {
			s := strings.TrimSpace(lines[ln-1])
			if !strings.HasPrefix(s, "--") {
				if s == "" {
					continue
				}
				break
			}
			body := strings.TrimSpace(strings.TrimPrefix(s, "--"))
			if k, v, ok := strings.Cut(body, ":"); ok && !strings.Contains(k, " ") {
				k = strings.ToLower(strings.TrimSpace(k))
				if _, exists := st.Tags[k]; !exists {
					st.Tags[k] = strings.TrimSpace(v)
				}
			}
		}
		st.Name = st.Tags["name"]
		out = append(out, st)
		cur = nil
	}
	for _, t := range toks {
		if t.Kind == TPunct && t.Text == ";" && t.Depth == 0 {
			flush(t.Line)
			continue
		}
		cur = append(cur, t)
	}
	flush(0)
	return out
}

func sliceText(lines []string, l1, c1, l2, c2 int) string {
	// columns are rune-agnostic byte offsets in our lexer (col advances per byte)
	if l1 < 1 || l2 > len(lines) {
		return ""
	}
	if l1 == l2 {
		ln := lines[l1-1]
		return safeSlice(ln, c1-1, c2-1)
	}
	var b strings.Builder
	b.WriteString(safeSlice(lines[l1-1], c1-1, len(lines[l1-1])))
	for l := l1 + 1; l < l2; l++ {
		b.WriteString("\n")
		b.WriteString(lines[l-1])
	}
	b.WriteString("\n")
	b.WriteString(safeSlice(lines[l2-1], 0, c2-1))
	return b.String()
}

func safeSlice(s string, a, b int) string {
	if a < 0 {
		a = 0
	}
	if b > len(s) {
		b = len(s)
	}
	if a > b {
		return ""
	}
	return s[a:b]
}
