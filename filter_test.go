package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const sampleFile = "testdata/sample.jsonl"

func path(segs ...any) []Seg {
	var p []Seg
	for _, s := range segs {
		switch s := s.(type) {
		case string:
			p = append(p, Seg{Key: s})
		case int:
			p = append(p, Seg{Index: s, IsIndex: true})
		}
	}
	return p
}

func TestFormatPath(t *testing.T) {
	cases := []struct {
		p    []Seg
		all  bool
		want string
	}{
		{nil, false, "."},
		{path("header", "sequenceNumber"), false, ".header.sequenceNumber"},
		{path("a", "record-type", "and"), false, `.a."record-type"."and"`},
		{path("a", 0, "b"), false, ".a[0].b"},
		{path("a", 0, "b"), true, ".a[].b"},
	}
	for _, c := range cases {
		if got := formatPath(c.p, c.all); got != c.want {
			t.Errorf("formatPath(%v, %v) = %s, want %s", c.p, c.all, got, c.want)
		}
	}
}

func TestCondExpr(t *testing.T) {
	got := condExpr(path("a", 0, "b", 2, "c"), `== "x"`, true, nil)
	want := `any(.a[]; any(.b[]; .c == "x"))`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := condExpr(path(0), "> 1", true, nil); got != "any(.[]; . > 1)" {
		t.Errorf("root array: %s", got)
	}
}

func TestOptional(t *testing.T) {
	b := builder{Opts: map[string]bool{
		".payload.records":                     true, // array: [] becomes []?
		".payload.records[].details.parties[]": true, // object: .role becomes .role?
	}}
	opt := b.optFlags(role)
	if got, want := formatPathOpt(role, false, opt), ".payload.records[0]?.details.parties[1].role?"; got != want {
		t.Errorf("exact: got %s, want %s", got, want)
	}
	b.Conds = []cond{{Path: role, All: true, Test: `== "x"`}}
	b.Projs = []proj{{Path: role, All: true}}
	want := `select(any(.payload.records[]?; any(.details.parties[]; .role? == "x"))) | [.payload.records[]?.details.parties[].role?]`
	if got := b.Filter(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := compile(b.Filter()); err != nil {
		t.Error(err)
	}
	c := b.clone()
	delete(c.Opts, ".payload.records")
	if !b.Opts[".payload.records"] {
		t.Error("clone shares the optional nodes")
	}
}

var role = path("payload", "records", 0, "details", "parties", 1, "role")

func TestBuilderFilter(t *testing.T) {
	b := builder{
		Conds: []cond{{Path: path("header", "sequenceNumber"), Test: "== 0"}},
		Projs: []proj{{Path: path("header", "session", "id")}},
	}
	want := "select(.header.sequenceNumber == 0) | .header.session.id"
	if got := b.Filter(); got != want {
		t.Errorf("got %s", got)
	}
	b.Projs = append(b.Projs, proj{Path: role, All: true}, proj{Path: path("x", "id")})
	got := b.Filter()
	if !strings.Contains(got, `"role": [.payload.records[]`) || !strings.Contains(got, `"id_2": .x.id`) {
		t.Errorf("multiple projections: %s", got)
	}
}

// TestAgainstJq checks that jqe and the real jq produce the same results.
func TestAgainstJq(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not found")
	}
	f, err := os.Open(sampleFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	items, err := loadItems(f)
	if err != nil {
		t.Fatal(err)
	}
	filters := []string{
		".",
		"select(.header.sequenceNumber == 0) | .header.session.id",
		"select(.header.sequenceNumber == 100)",
		"select(" + condExpr(role, `== "terminating"`, true, nil) + ")",
		builder{Opts: map[string]bool{".payload.records": true}, Conds: []cond{{Path: role, All: true, Test: `== "terminating"`}}}.Filter(),
		builder{Projs: []proj{{Path: role, All: true}, {Path: path("header", "timeStamp")}}}.Filter(),
	}
	for _, filter := range filters {
		code, err := compile(filter)
		if err != nil {
			t.Fatalf("%s: %v", filter, err)
		}
		var ours bytes.Buffer
		if err := writeAll(&ours, code, items, jqOpts{}); err != nil {
			t.Fatal(err)
		}
		want, err := exec.Command(jq, "-cS", filter, sampleFile).Output()
		if err != nil {
			t.Fatalf("jq %s: %v", filter, err)
		}
		cmd := exec.Command(jq, "-cS", ".")
		cmd.Stdin = &ours
		got, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s\njqe: %s\njq:  %s", filter, got, want)
		}
	}
}

// TestKeyOrder checks that unchanged input values keep their original key order.
func TestKeyOrder(t *testing.T) {
	items, err := loadItems(strings.NewReader(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := compile("select(.a == 2)")
	var out bytes.Buffer
	if err := writeAll(&out, code, items, jqOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "{\"b\":1,\"a\":2}\n" {
		t.Errorf("got %q", got)
	}
}

func TestRunFilter(t *testing.T) {
	items, err := loadItems(strings.NewReader(`{"b":1,"a":2} {"b":3,"a":4}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"{", `  "b": 3,`, `  "a": 4`, "}"}
	check := func(t *testing.T, res runResult) {
		if res.Err != "" {
			t.Fatalf("%s: unexpected error %q", res.Engine, res.Err)
		}
		got := make([]string, len(res.Lines))
		for i, l := range res.Lines {
			got[i] = ansi.Strip(l)
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: got %q", res.Engine, got)
		}
	}
	t.Run("gojq", func(t *testing.T) {
		t.Setenv("PATH", "")
		res := runFilter(context.Background(), "select(.a > 2)", jqOpts{}, items)
		if res.Engine != "gojq" {
			t.Fatalf("engine %s", res.Engine)
		}
		check(t, res)
		if res := runFilter(context.Background(), ".a | error", jqOpts{}, items); !strings.Contains(res.Err, "error") {
			t.Errorf("missing error: %q", res.Err)
		}
	})
	t.Run("jq", func(t *testing.T) {
		if _, err := exec.LookPath("jq"); err != nil {
			t.Skip("jq not found")
		}
		check(t, runFilter(context.Background(), "select(.a > 2)", jqOpts{}, items))
	})
}

func TestExport(t *testing.T) {
	b := builder{
		Conds:  []cond{{Path: path("header", "sequenceNumber"), Test: "> 0"}},
		Projs:  []proj{{Path: path("header", "session", "id")}, {Path: role, All: true}},
		Export: "csv",
	}
	row := `select(.header.sequenceNumber > 0) | [.header.session.id, ([.payload.records[].details.parties[].role] | join(";"))] | @csv`
	if got := b.Filter(); got != row {
		t.Errorf("csv:\ngot  %s\nwant %s", got, row)
	}
	if o := b.opts(); !o.Raw || o.Null {
		t.Errorf("csv options: %+v", o)
	}
	b.Header = true
	want := `["id", "role"], (inputs | ` + strings.TrimSuffix(row, " | @csv") + `) | @csv`
	if got := b.Filter(); got != want {
		t.Errorf("csv+header:\ngot  %s\nwant %s", got, want)
	}
	if b.RowFilter() != row {
		t.Errorf("row filter: %s", b.RowFilter())
	}
	if b.Projs = nil; b.Filter() != "select(.header.sequenceNumber > 0)" || b.opts().args() != nil {
		t.Errorf("no projection: %s %v", b.Filter(), b.opts())
	}
}

// TestExportAgainstJq checks the export filters with the real jq and its options.
func TestExportAgainstJq(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not found")
	}
	f, err := os.Open(sampleFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	items, err := loadItems(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"csv", "tsv"} {
		for _, header := range []bool{false, true} {
			b := builder{
				Projs:  []proj{{Path: path("header", "session", "id")}, {Path: path("header", "session", "region")}, {Path: role, All: true}},
				Export: format, Header: header,
			}
			o := b.opts()
			code, err := compileOpts(b.Filter(), o, items)
			if err != nil {
				t.Fatalf("%s: %v", b.Filter(), err)
			}
			var ours bytes.Buffer
			if err := writeAll(&ours, code, items, o); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(jq, append(o.args(), b.Filter(), sampleFile)...).Output()
			if err != nil {
				t.Fatalf("jq %s: %v", b.Filter(), err)
			}
			want := bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n")) // jq on Windows
			if ours.String() != string(want) {
				t.Errorf("%s\njqe: %q\njq:  %q", b.Filter(), ours.String(), want)
			}
			t.Run("run "+format, func(t *testing.T) {
				t.Setenv("PATH", "")
				res := runFilter(context.Background(), b.Filter(), o, items)
				if res.Err != "" || strings.Join(res.Lines, "\n")+"\n" != string(want) {
					t.Errorf("gojq run: %q %q", res.Lines, res.Err)
				}
			})
		}
	}
}
