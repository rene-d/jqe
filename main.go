// jqe is an interactive TUI editor for jq filters over a stream of JSON values.
//
// The UI is drawn on the terminal; on exit (q) the filter alone is written to
// stdout, so that: jq "$(jqe data.json)" data.json
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	initial := flag.String("e", "", "initial jq filter (edited by hand)")
	output := flag.String("o", "", "write the final filter's results to this file (one value per line, like jq -c)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: jqe [-e filter] [-o output.json] [file.json | -]\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if err := run(flag.Arg(0), *initial, *output); err != nil {
		fmt.Fprintln(os.Stderr, "jqe:", err)
		os.Exit(1)
	}
}

func run(path, initial, output string) error {
	var r io.Reader = os.Stdin
	name := "stdin"
	if path != "" && path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		r, name = bufio.NewReader(f), path
	}
	items, err := loadItems(r)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("no JSON value in input")
	}

	var b builder
	if initial != "" {
		if _, err := compile(initial); err != nil {
			return fmt.Errorf("invalid initial filter: %w", err)
		}
		b = builder{IsRaw: true, Raw: initial}
	}

	// Draw on and read keys from the terminal itself (like fzf): stdout is left
	// for the filter, even when data comes from stdin.
	var out io.Writer = os.Stderr
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		out = tty
	}
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithOutput(out), tea.WithInputTTY()}
	final, err := tea.NewProgram(newModel(name, items, b), opts...).Run()
	if err != nil {
		return err
	}
	m := final.(*model)
	if !m.printOnExit {
		return nil
	}

	filter := m.b.Filter()
	fmt.Println(filter)
	checkWithJq(filter)
	file := name
	if path == "" || path == "-" {
		file = "-"
	}
	fmt.Fprintf(os.Stderr, "jq %s %s\n", shellQuote(filter), shellQuote(file))

	if output != "" {
		code, err := compile(filter)
		if err != nil {
			return err
		}
		f, err := os.Create(output)
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		if err := writeAll(w, code, items); err != nil {
			f.Close()
			return err
		}
		if err := w.Flush(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Results written to %s\n", output)
	}
	return nil
}

// checkWithJq checks that the real jq accepts the filter (gojq differs on a
// few functions). jq exits with code 3 on a compile error.
func checkWithJq(filter string) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, jq, "-n", filter)
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 3 {
		fmt.Fprintf(os.Stderr, "Warning: jq rejects this filter:\n%s", stderr.String())
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
