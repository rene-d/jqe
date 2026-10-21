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
	got := condExpr(path("a", 0, "b", 2, "c"), `== "x"`, true)
	want := `any(.a[]; any(.b[]; .c == "x"))`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := condExpr(path(0), "> 1", true); got != "any(.[]; . > 1)" {
		t.Errorf("root array: %s", got)
	}
}

var role = path("payload", "records", 0, "details", "parties", 1, "role")

func TestBuilderFilter(t *testing.T) {
	b := builder{
		Conds: []cond{{Text: "select(" + condExpr(path("header", "sequenceNumber"), "== 0", false) + ")"}},
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
		"select(" + condExpr(role, `== "terminating"`, true) + ")",
		builder{Projs: []proj{{Path: role, All: true}, {Path: path("header", "timeStamp")}}}.Filter(),
	}
	for _, filter := range filters {
		code, err := compile(filter)
		if err != nil {
			t.Fatalf("%s: %v", filter, err)
		}
		var ours bytes.Buffer
		if err := writeAll(&ours, code, items); err != nil {
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
	if err := writeAll(&out, code, items); err != nil {
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
		res := runFilter(context.Background(), "select(.a > 2)", items)
		if res.Engine != "gojq" {
			t.Fatalf("engine %s", res.Engine)
		}
		check(t, res)
		if res := runFilter(context.Background(), ".a | error", items); !strings.Contains(res.Err, "error") {
			t.Errorf("missing error: %q", res.Err)
		}
	})
	t.Run("jq", func(t *testing.T) {
		if _, err := exec.LookPath("jq"); err != nil {
			t.Skip("jq not found")
		}
		check(t, runFilter(context.Background(), "select(.a > 2)", items))
	})
}
