package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
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
