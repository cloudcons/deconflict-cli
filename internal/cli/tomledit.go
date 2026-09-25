package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A conservative reader for Codex's config.toml, enough to edit two entries in
// it without a TOML library (this module has no dependencies) and without a
// round-trip that would reformat a file the user comments in.
//
// It does not interpret values. It finds where each statement starts and ends,
// which table it belongs to and what its full key is — the questions an edit
// has to answer. Everything it does not understand is an error, and the
// installer turns an error into "add this yourself", because the failure it
// replaces was worse: plain text matching wrote a second [features] table after
// `[features] # flags`, split a multi-line array in half, and appended
// `args = ["mcp"]` onto the next table's header line. Codex refuses to start on
// any of those, and the install said it had succeeded.

type tomlKind int

const (
	tomlTable      tomlKind = iota // [a.b]
	tomlArrayTable                 // [[a.b]]
	tomlKeyValue                   // a.b = value
)

// tomlStmt is one statement: a header or a key/value pair, spanning lines
// first..last (inclusive, 0-based).
type tomlStmt struct {
	kind        tomlKind
	first, last int
	path        []string // header: the table; key/value: table + key
	table       []string // key/value: the table it sits in
	key         []string // key/value: the key as written, dotted parts split
	value       string   // key/value: the value's text, comment excluded
	inArray     bool     // key/value inside an [[array]] element
}

type tomlDoc struct {
	lines []string // each with its line ending, so joining them is the file
	stmts []tomlStmt
}

func parseTOMLDoc(text string) (*tomlDoc, error) {
	d := &tomlDoc{lines: strings.SplitAfter(text, "\n")}
	if n := len(d.lines); n > 0 && d.lines[n-1] == "" {
		d.lines = d.lines[:n-1]
	}
	starts := make([]int, len(d.lines))
	off := 0
	for i, l := range d.lines {
		starts[i] = off
		off += len(l)
	}
	lineOf := func(pos int) int {
		lo, hi := 0, len(starts)-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if starts[mid] <= pos {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return lo
	}

	s := &tomlScan{s: text}
	var table []string
	inArray := false
	for {
		s.spaces()
		if s.eof() {
			break
		}
		start := s.i
		switch c := s.s[s.i]; {
		case c == '\n' || c == '\r':
			if err := s.eol(); err != nil {
				return nil, s.errAt(err)
			}
			continue
		case c == '#':
			s.comment()
			if err := s.eol(); err != nil {
				return nil, s.errAt(err)
			}
			continue
		case c == '[':
			kind := tomlTable
			s.i++
			if !s.eof() && s.s[s.i] == '[' {
				kind = tomlArrayTable
				s.i++
			}
			s.spaces()
			key, err := s.key()
			if err != nil {
				return nil, s.errAt(err)
			}
			s.spaces()
			closing := "]"
			if kind == tomlArrayTable {
				closing = "]]"
			}
			if !strings.HasPrefix(s.s[s.i:], closing) {
				return nil, s.errAt(fmt.Errorf("expected %s", closing))
			}
			s.i += len(closing)
			last := s.i - 1
			if err := s.trailer(); err != nil {
				return nil, s.errAt(err)
			}
			table, inArray = key, kind == tomlArrayTable
			d.stmts = append(d.stmts, tomlStmt{kind: kind, first: lineOf(start), last: lineOf(last), path: key})
		default:
			key, err := s.key()
			if err != nil {
				return nil, s.errAt(err)
			}
			s.spaces()
			if s.eof() || s.s[s.i] != '=' {
				return nil, s.errAt(errors.New("expected ="))
			}
			s.i++
			s.spaces()
			vstart := s.i
			if err := s.value(); err != nil {
				return nil, s.errAt(err)
			}
			value := strings.TrimSpace(s.s[vstart:s.i])
			last := s.i - 1
			if err := s.trailer(); err != nil {
				return nil, s.errAt(err)
			}
			d.stmts = append(d.stmts, tomlStmt{
				kind: tomlKeyValue, first: lineOf(start), last: lineOf(last),
				path: append(append([]string{}, table...), key...), table: table,
				key: key, value: value, inArray: inArray,
			})
		}
	}
	return d, nil
}

func (d *tomlDoc) String() string { return strings.Join(d.lines, "") }

// lineEnding is what the file uses, so an inserted line matches its neighbours.
func (d *tomlDoc) lineEnding() string {
	for _, l := range d.lines {
		if strings.HasSuffix(l, "\r\n") {
			return "\r\n"
		}
	}
	return "\n"
}

// splice replaces lines first..last with the given ones (each without its
// ending). A first past the end appends. Callers splice from the bottom up so
// earlier line numbers stay valid.
func (d *tomlDoc) splice(first, last int, repl []string) {
	nl := d.lineEnding()
	// A file whose last line has no newline would have the next line glued to
	// it — which is how `args = ["mcp"][mcp_servers.other]` came about.
	if first > 0 && first-1 < len(d.lines) && !strings.HasSuffix(d.lines[first-1], "\n") {
		d.lines[first-1] += nl
	}
	var out []string
	for _, r := range repl {
		out = append(out, r+nl)
	}
	tail := []string{}
	if last+1 < len(d.lines) {
		tail = d.lines[last+1:]
	}
	head := d.lines[:min(first, len(d.lines))]
	d.lines = append(append(append([]string{}, head...), out...), tail...)
}

// sectionEnd is the last line belonging to the table whose header is stmt
// index h: its last key/value, or the header itself when it has none. Comments
// and blank lines before the next header are left with the next header, since
// that is usually who they describe.
func (d *tomlDoc) sectionEnd(h int) int {
	last := d.stmts[h].last
	for _, st := range d.stmts[h+1:] {
		if st.kind != tomlKeyValue {
			break
		}
		last = st.last
	}
	return last
}

// ---------- scanning ----------

type tomlScan struct {
	s string
	i int
}

func (s *tomlScan) eof() bool { return s.i >= len(s.s) }

func (s *tomlScan) errAt(err error) error {
	line := strings.Count(s.s[:min(s.i, len(s.s))], "\n") + 1
	return fmt.Errorf("line %d: %w", line, err)
}

func (s *tomlScan) spaces() {
	for !s.eof() && (s.s[s.i] == ' ' || s.s[s.i] == '\t') {
		s.i++
	}
}

func (s *tomlScan) comment() {
	for !s.eof() && s.s[s.i] != '\n' {
		s.i++
	}
}

func (s *tomlScan) eol() error {
	switch {
	case s.eof():
		return nil
	case s.s[s.i] == '\n':
		s.i++
		return nil
	case strings.HasPrefix(s.s[s.i:], "\r\n"):
		s.i += 2
		return nil
	}
	return fmt.Errorf("unexpected %q", s.s[s.i])
}

// trailer is what may follow a statement on its line: spaces and a comment.
func (s *tomlScan) trailer() error {
	s.spaces()
	if !s.eof() && s.s[s.i] == '#' {
		s.comment()
	}
	if err := s.eol(); err != nil {
		return fmt.Errorf("unexpected text after the statement")
	}
	return nil
}

// key reads a possibly dotted key, returning its decoded parts.
func (s *tomlScan) key() ([]string, error) {
	var parts []string
	for {
		s.spaces()
		if s.eof() {
			return nil, errors.New("expected a key")
		}
		switch c := s.s[s.i]; {
		case c == '"':
			raw, err := s.basicString()
			if err != nil {
				return nil, err
			}
			part, err := strconv.Unquote(raw)
			if err != nil {
				return nil, fmt.Errorf("cannot read the key %s", raw)
			}
			parts = append(parts, part)
		case c == '\'':
			raw, err := s.literalString()
			if err != nil {
				return nil, err
			}
			parts = append(parts, raw[1:len(raw)-1])
		default:
			start := s.i
			for !s.eof() && isBareKey(s.s[s.i]) {
				s.i++
			}
			if s.i == start {
				return nil, fmt.Errorf("expected a key, found %q", s.s[s.i])
			}
			parts = append(parts, s.s[start:s.i])
		}
		s.spaces()
		if s.eof() || s.s[s.i] != '.' {
			return parts, nil
		}
		s.i++
	}
}

func isBareKey(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (s *tomlScan) basicString() (string, error) {
	start := s.i
	s.i++
	for !s.eof() {
		switch s.s[s.i] {
		case '\\':
			s.i += 2
			continue
		case '"':
			s.i++
			return s.s[start:s.i], nil
		case '\n':
			return "", errors.New("unterminated string")
		}
		s.i++
	}
	return "", errors.New("unterminated string")
}

func (s *tomlScan) literalString() (string, error) {
	start := s.i
	s.i++
	for !s.eof() {
		switch s.s[s.i] {
		case '\'':
			s.i++
			return s.s[start:s.i], nil
		case '\n':
			return "", errors.New("unterminated string")
		}
		s.i++
	}
	return "", errors.New("unterminated string")
}

// multiline skips a """ or ”' string, which may hold newlines, # and
// brackets — all of which would otherwise be mistaken for structure.
func (s *tomlScan) multiline(delim string) error {
	s.i += 3
	for !s.eof() {
		if delim == `"""` && s.s[s.i] == '\\' {
			s.i += 2
			continue
		}
		if strings.HasPrefix(s.s[s.i:], delim) {
			s.i += 3
			// Up to two more quotes may close the string: """a"""" is `a"`.
			for n := 0; n < 2 && !s.eof() && s.s[s.i] == delim[0]; n++ {
				s.i++
			}
			return nil
		}
		s.i++
	}
	return errors.New("unterminated multi-line string")
}

func (s *tomlScan) str() error {
	switch {
	case strings.HasPrefix(s.s[s.i:], `"""`):
		return s.multiline(`"""`)
	case strings.HasPrefix(s.s[s.i:], `'''`):
		return s.multiline(`'''`)
	case s.s[s.i] == '"':
		_, err := s.basicString()
		return err
	default:
		_, err := s.literalString()
		return err
	}
}

// value skips one value. Arrays and inline tables are followed to their
// closing bracket across lines, stepping over strings and comments inside them.
func (s *tomlScan) value() error {
	if s.eof() {
		return errors.New("expected a value")
	}
	switch s.s[s.i] {
	case '"', '\'':
		return s.str()
	case '[', '{':
		depth := 0
		for !s.eof() {
			switch c := s.s[s.i]; c {
			case '"', '\'':
				if err := s.str(); err != nil {
					return err
				}
				continue
			case '#':
				s.comment()
				continue
			case '[', '{':
				depth++
			case ']', '}':
				depth--
				if depth == 0 {
					s.i++
					return nil
				}
			}
			s.i++
		}
		return errors.New("unclosed array or inline table")
	default:
		// Numbers, booleans and dates. A local date-time may contain a space,
		// so the value runs to a comment or the end of the line.
		start := s.i
		for !s.eof() && s.s[s.i] != '#' && s.s[s.i] != '\n' && s.s[s.i] != '\r' {
			s.i++
		}
		v := strings.TrimSpace(s.s[start:s.i])
		if v == "" {
			return errors.New("expected a value")
		}
		for _, c := range v {
			if strings.ContainsRune("=[]{}\"'", c) {
				return fmt.Errorf("cannot read the value %q", v)
			}
		}
		// Leave the cursor after the value itself, not after trailing spaces,
		// so the statement's last character is part of it.
		s.i = start + len(strings.TrimRight(s.s[start:s.i], " \t"))
		return nil
	}
}

// ---------- paths ----------

func pathEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pathHasPrefix(p, prefix []string) bool {
	return len(p) >= len(prefix) && pathEq(p[:len(prefix)], prefix)
}
