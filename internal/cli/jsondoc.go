package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// An ordered, lossless JSON document, for editing files somebody else owns.
//
// encoding/json into map[string]any is the obvious way to add a key to a
// settings file, and it is wrong in three ways that each showed up on a real
// machine: every key comes back in alphabetical order, so a one-line change is
// a whole-file diff; numbers go through float64, so 12345678901234567890 comes
// back as 12345678901234567000; and &, < and > inside other people's hook
// commands are rewritten as \u0026, \u003c and \u003e. ~/.claude.json is thousands of
// lines Claude Code rewrites constantly, and the installer has one key to add
// to it.
//
// So objects keep their key order, and every scalar keeps the exact text it was
// written as. Only what this installer touches is re-encoded.

// jobj is a JSON object in document order.
type jobj struct {
	keys []string          // decoded names, in order
	raw  map[string]string // name -> the key exactly as written, quotes included
	vals map[string]any    // *jobj, []any or jraw
}

// jraw is a scalar as it appears in the file: a string with its quotes, a
// number, true, false or null.
type jraw string

func newJobj() *jobj {
	return &jobj{raw: map[string]string{}, vals: map[string]any{}}
}

func (o *jobj) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// set replaces a value in place, or appends the key at the end — where a
// person adding it by hand would most likely have put it.
func (o *jobj) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
		o.raw[key] = string(jstr(key))
	}
	o.vals[key] = v
}

func (o *jobj) del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	delete(o.raw, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// jstr encodes a string the way a person would write it: & stays &, not \u0026.
func jstr(s string) jraw {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return jraw(strings.TrimSuffix(buf.String(), "\n"))
}

func jnum(n int) jraw { return jraw(strconv.Itoa(n)) }

// jstring reads a value as a string, reporting false for anything else.
func jstring(v any) (string, bool) {
	r, ok := v.(jraw)
	if !ok || !strings.HasPrefix(string(r), `"`) {
		return "", false
	}
	var s string
	if err := json.Unmarshal([]byte(r), &s); err != nil {
		return "", false
	}
	return s, true
}

// jsonDoc is a parsed file plus what is needed to write it back the way it came.
type jsonDoc struct {
	root   *jobj
	indent string
	orig   string // the encoding of the document as read, for "did anything change"
}

// parseJSONDoc reads a whole file. An empty or missing file is an empty object,
// because every caller is about to add its own entry to it.
func parseJSONDoc(text string) (*jsonDoc, error) {
	if strings.TrimSpace(text) == "" {
		d := &jsonDoc{root: newJobj(), indent: "  "}
		d.orig = d.encode()
		return d, nil
	}
	// Validating first means the parser below never has to report a syntax
	// error of its own, and the message is the standard library's.
	if !json.Valid([]byte(text)) {
		var probe any
		err := json.Unmarshal([]byte(text), &probe)
		if err == nil {
			err = errors.New("invalid JSON")
		}
		return nil, err
	}
	p := &jparser{s: text}
	p.ws()
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return nil, errors.New("the top level is not a JSON object")
	}
	root, err := p.object()
	if err != nil {
		return nil, err
	}
	d := &jsonDoc{root: root, indent: detectIndent(text)}
	d.orig = d.encode()
	return d, nil
}

// changed reports whether the document now encodes differently from how it was
// read. Unchanged documents are not written at all, so a file whose formatting
// differs from ours is left byte-for-byte alone unless there is a reason.
func (d *jsonDoc) changed() bool { return d.encode() != d.orig }

func (d *jsonDoc) encode() string {
	var b strings.Builder
	writeJSON(&b, d.root, d.indent, 0)
	b.WriteString("\n")
	return b.String()
}

// detectIndent takes the indentation of the first indented line, so a file
// written with four spaces or tabs keeps them.
func detectIndent(text string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}

func writeJSON(b *strings.Builder, v any, indent string, depth int) {
	pad := strings.Repeat(indent, depth)
	switch t := v.(type) {
	case *jobj:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(pad + indent + t.raw[k] + ": ")
			writeJSON(b, t.vals[k], indent, depth+1)
			if i < len(t.keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(pad + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(pad + indent)
			writeJSON(b, e, indent, depth+1)
			if i < len(t)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(pad + "]")
	case jraw:
		b.WriteString(string(t))
	default:
		panic(fmt.Sprintf("jsondoc: unexpected %T", v))
	}
}

// jparser walks text json.Valid has already accepted, so it only has to know
// where values end, not whether they are well formed.
type jparser struct {
	s string
	i int
}

func (p *jparser) ws() {
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *jparser) value() (any, error) {
	p.ws()
	switch p.s[p.i] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		return jraw(p.str()), nil
	default:
		start := p.i
		for p.i < len(p.s) && strings.IndexByte(",]} \t\r\n", p.s[p.i]) < 0 {
			p.i++
		}
		return jraw(p.s[start:p.i]), nil
	}
}

func (p *jparser) str() string {
	start := p.i
	p.i++
	for p.s[p.i] != '"' {
		if p.s[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	p.i++
	return p.s[start:p.i]
}

func (p *jparser) object() (*jobj, error) {
	o := newJobj()
	p.i++ // {
	p.ws()
	if p.s[p.i] == '}' {
		p.i++
		return o, nil
	}
	for {
		p.ws()
		rawKey := p.str()
		var key string
		if err := json.Unmarshal([]byte(rawKey), &key); err != nil {
			return nil, err
		}
		// A repeated key is legal JSON that every reader resolves differently.
		// Keeping one of them would silently drop the other.
		if _, dup := o.vals[key]; dup {
			return nil, fmt.Errorf("the key %s appears twice in one object", rawKey)
		}
		p.ws()
		p.i++ // :
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		o.keys = append(o.keys, key)
		o.raw[key] = rawKey
		o.vals[key] = v
		p.ws()
		if p.s[p.i] == ',' {
			p.i++
			continue
		}
		p.i++ // }
		return o, nil
	}
}

func (p *jparser) array() ([]any, error) {
	out := []any{}
	p.i++ // [
	p.ws()
	if p.s[p.i] == ']' {
		p.i++
		return out, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		p.ws()
		if p.s[p.i] == ',' {
			p.i++
			continue
		}
		p.i++ // ]
		return out, nil
	}
}
