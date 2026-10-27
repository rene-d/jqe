package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/itchyny/gojq"
)

const (
	runTimeout  = 30 * time.Second
	maxRunLines = 100000
)

// runResult is the full output of the filter over the whole input, as jq prints it.
type runResult struct {
	Lines  []string
	Engine string // "jq-1.8.2" or "gojq"
	Err    string // engine errors (stderr, exit status)
}

// runFilter runs the filter on every input value with the real jq when it is
// installed (colored, original key order, exactly what the exported filter
// will do), or with gojq otherwise. o gives the jq options the filter needs.
func runFilter(ctx context.Context, filter string, o jqOpts, items []Item) runResult {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	var res runResult
	var out bytes.Buffer
	if jq, err := exec.LookPath("jq"); err == nil {
		res.Engine = "jq"
		if v, err := exec.CommandContext(ctx, jq, "--version").Output(); err == nil {
			res.Engine = strings.TrimSpace(string(v))
		}
		var in bytes.Buffer
		for _, it := range items {
			in.Write(it.Raw)
			in.WriteByte('\n')
		}
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, jq, append(append([]string{"-C"}, o.args()...), filter)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = &in, &out, &stderr
		err := cmd.Run()
		res.Err = strings.TrimSpace(stderr.String())
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			res.Err = strings.TrimSpace(res.Err + "\naborted after " + runTimeout.String())
		case errors.As(err, &ee):
			res.Err = strings.TrimSpace(fmt.Sprintf("%s\nexit status %d", res.Err, ee.ExitCode()))
		case err != nil:
			res.Err = err.Error()
		}
	} else {
		res.Engine = "gojq"
		code, err := compileOpts(filter, o, items)
		if err != nil {
			res.Err = err.Error()
			return res
		}
		var errs []string
		for _, it := range inputsFor(o, items) {
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
					// Like jq: report the error and go on with the next input.
					errs = append(errs, "error: "+err.Error())
					break
				}
				if s, ok := v.(string); ok && o.Raw {
					out.WriteString(s + "\n")
					continue
				}
				var buf bytes.Buffer
				if err := json.Indent(&buf, []byte(encode(v, it, false)), "", "  "); err != nil {
					errs = append(errs, "error: "+err.Error())
					continue
				}
				buf.WriteByte('\n')
				out.Write(buf.Bytes())
			}
			if ctx.Err() != nil {
				errs = append(errs, "aborted after "+runTimeout.String())
				break
			}
		}
		res.Err = strings.Join(errs, "\n")
	}
	text := strings.ReplaceAll(out.String(), "\r\n", "\n") // jq on Windows
	res.Lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if out.Len() == 0 {
		res.Lines = nil
	}
	if len(res.Lines) > maxRunLines {
		res.Lines = append(res.Lines[:maxRunLines], fmt.Sprintf("… output truncated to %d lines", maxRunLines))
	}
	return res
}
