package main

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/itchyny/gojq"
)

// Seg is a path segment: an object key or an array index.
type Seg struct {
	Key     string
	Index   int
	IsIndex bool
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// jq keywords are quoted to stay compatible with older jq versions.
var jqKeywords = map[string]bool{
	"and": true, "or": true, "not": true, "if": true, "then": true, "elif": true,
	"else": true, "end": true, "as": true, "def": true, "reduce": true,
	"foreach": true, "try": true, "catch": true, "label": true, "import": true,
	"include": true, "__loc__": true,
}

// formatPath writes a path in jq syntax; with all, indices become [].
func formatPath(p []Seg, all bool) string {
	if len(p) == 0 {
		return "."
	}
	var sb strings.Builder
	for _, s := range p {
		switch {
		case s.IsIndex && all:
			sb.WriteString("[]")
		case s.IsIndex:
			fmt.Fprintf(&sb, "[%d]", s.Index)
		case identRe.MatchString(s.Key) && !jqKeywords[s.Key]:
			sb.WriteString("." + s.Key)
		default:
			sb.WriteString("." + quoteJSON(s.Key))
		}
	}
	return sb.String()
}

func hasIndex(p []Seg) bool {
	for _, s := range p {
		if s.IsIndex {
			return true
		}
	}
	return false
}

// condExpr builds the boolean expression "path + test". In all mode, each
// array level becomes any(gen; cond): one matching element is enough.
func condExpr(p []Seg, test string, all bool) string {
	if all {
		for i, s := range p {
			if s.IsIndex {
				gen := formatPath(p[:i], false)
				return fmt.Sprintf("any(%s[]; %s)", gen, condExpr(p[i+1:], test, all))
			}
		}
	}
	return formatPath(p, false) + " " + test
}

func projExpr(p proj) string {
	if p.All && hasIndex(p.Path) {
		return "[" + formatPath(p.Path, true) + "]"
	}
	return formatPath(p.Path, false)
}

// projName returns a projection's field name: the last key of the path.
func projName(p []Seg) string {
	for i := len(p) - 1; i >= 0; i-- {
		if !p[i].IsIndex {
			return p[i].Key
		}
	}
	return "value"
}

type cond struct {
	Path []Seg
	All  bool
	Text string // full expression: select(...)
}

type proj struct {
	Path []Seg
	All  bool
}

// builder is the state of the filter built from the UI.
type builder struct {
	Conds []cond
	Projs []proj
	IsRaw bool   // filter edited by hand
	Raw   string // text of the hand-edited filter
}

func (b builder) clone() builder {
	c := b
	c.Conds = append([]cond(nil), b.Conds...)
	c.Projs = append([]proj(nil), b.Projs...)
	return c
}

func (b builder) Filter() string {
	if b.IsRaw {
		return b.Raw
	}
	var parts []string
	for _, c := range b.Conds {
		parts = append(parts, c.Text)
	}
	switch len(b.Projs) {
	case 0:
	case 1:
		parts = append(parts, projExpr(b.Projs[0]))
	default:
		seen := map[string]int{}
		var fields []string
		for _, p := range b.Projs {
			name := projName(p.Path)
			seen[name]++
			if n := seen[name]; n > 1 {
				name = fmt.Sprintf("%s_%d", name, n)
			}
			fields = append(fields, quoteJSON(name)+": "+projExpr(p))
		}
		parts = append(parts, "{"+strings.Join(fields, ", ")+"}")
	}
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, " | ")
}

func compile(filter string) (*gojq.Code, error) {
	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, err
	}
	return gojq.Compile(q)
}

// sameObject reports whether the output is the input object itself (as with select).
func sameObject(out, in any) bool {
	mo, ok1 := out.(map[string]any)
	mi, ok2 := in.(map[string]any)
	return ok1 && ok2 && reflect.ValueOf(mo).UnsafePointer() == reflect.ValueOf(mi).UnsafePointer()
}

// encode renders an output as compact JSON; an unchanged input object keeps
// its key order (gojq sorts keys).
func encode(v any, it Item) string {
	if sameObject(v, it.Value) {
		return string(it.Raw)
	}
	b, err := gojq.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return string(b)
}

const maxOutsPerItem = 20

type itemResult struct {
	Outs []string
	More bool // outputs truncated
	Err  string
}

func (r itemResult) kept() bool { return len(r.Outs) > 0 || r.Err != "" }

// evaluate applies the filter to each value separately, like jq without -s.
func evaluate(ctx context.Context, code *gojq.Code, items []Item) ([]itemResult, error) {
	res := make([]itemResult, len(items))
	for i, it := range items {
		iter := code.RunWithContext(ctx, it.Value)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				if he, ok := err.(*gojq.HaltError); ok && he.Value() == nil {
					break
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				res[i].Err = err.Error()
				break
			}
			if len(res[i].Outs) == maxOutsPerItem {
				res[i].More = true
				break
			}
			res[i].Outs = append(res[i].Outs, encode(v, it))
		}
	}
	return res, nil
}

// writeAll writes every output of the filter, one per line, like jq -c.
func writeAll(w io.Writer, code *gojq.Code, items []Item) error {
	for _, it := range items {
		iter := code.Run(it.Value)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				if he, ok := err.(*gojq.HaltError); ok && he.Value() == nil {
					break
				}
				return err
			}
			if _, err := fmt.Fprintln(w, encode(v, it)); err != nil {
				return err
			}
		}
	}
	return nil
}
