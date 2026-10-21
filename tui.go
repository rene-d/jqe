package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const evalTimeout = 10 * time.Second

var (
	stHeader  = lipgloss.NewStyle().Reverse(true).Bold(true)
	stFilter  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	stErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	stDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stCursor  = lipgloss.NewStyle().Reverse(true)
	stKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stString  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	stNumber  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	stNull    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stCondMk  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	stProjMk  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	stMessage = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

type viewMode int

const (
	modeList viewMode = iota
	modeTree
)

type inputKind int

const (
	inputNone inputKind = iota
	inputCond
	inputRaw
)

type evalMsg struct {
	gen     int
	results []itemResult
	err     error
}

type treeLine struct {
	Path  []Seg
	Depth int
	Label string
	Node  *Node
}

type model struct {
	file  string
	items []Item

	b       builder
	undo    []builder
	allMode bool // arrays: any element ([]) rather than the exact index

	results    []itemResult
	matched    []int // indices of values kept by the filter
	compileErr string
	evalGen    int
	evaluating bool
	cancel     context.CancelFunc

	mode             viewMode
	listCur, listOff int

	treeItem         int
	tree             *Node
	collapsed        map[string]bool
	lines            []treeLine
	treeCur, treeOff int

	input     textinput.Model
	inputKind inputKind
	condPath  []Seg
	condAll   bool

	msg           string
	width, height int
	printOnExit   bool
}

func newModel(file string, items []Item, b builder) *model {
	ti := textinput.New()
	ti.Prompt = ""
	return &model{file: file, items: items, b: b, allMode: true, input: ti, width: 80, height: 24}
}

func (m *model) Init() tea.Cmd { return m.startEval(m.b.Filter()) }

// startEval runs the evaluation in the background; a newer one cancels the previous one.
func (m *model) startEval(filter string) tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	m.evalGen++
	gen := m.evalGen
	code, err := compile(filter)
	if err != nil {
		m.compileErr = err.Error()
		m.evaluating = false
		return nil
	}
	m.compileErr = ""
	m.evaluating = true
	ctx, cancel := context.WithTimeout(context.Background(), evalTimeout)
	m.cancel = cancel
	items := m.items
	return func() tea.Msg {
		defer cancel()
		res, err := evaluate(ctx, code, items)
		return evalMsg{gen: gen, results: res, err: err}
	}
}

func (m *model) setBuilder(b builder) tea.Cmd {
	m.undo = append(m.undo, m.b.clone())
	m.b = b
	return m.startEval(m.b.Filter())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case evalMsg:
		if msg.gen != m.evalGen {
			return m, nil
		}
		m.evaluating = false
		if msg.err != nil {
			m.compileErr = "evaluation aborted: " + msg.err.Error()
			return m, nil
		}
		m.results = msg.results
		m.matched = m.matched[:0]
		for i, r := range m.results {
			if r.kept() {
				m.matched = append(m.matched, i)
			}
		}
		m.listCur = clamp(m.listCur, 0, len(m.matched)-1)
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.inputKind != inputNone {
			return m.updateInput(msg)
		}
		m.msg = ""
		// Fast keystrokes sometimes arrive grouped ("kkp"): replay them one by one.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
			var cmds []tea.Cmd
			for _, r := range msg.Runes {
				_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
		return m.updateKeys(msg)
	}
	return m, nil
}

func (m *model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		kind := m.inputKind
		m.inputKind = inputNone
		m.input.Blur()
		if kind == inputRaw {
			return m, m.startEval(m.b.Filter())
		}
		return m, nil
	case "ctrl+a":
		if m.inputKind == inputCond {
			m.condAll = !m.condAll
			return m, nil
		}
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		switch m.inputKind {
		case inputCond:
			if text == "" {
				m.inputKind = inputNone
				return m, nil
			}
			all := m.condAll && hasIndex(m.condPath)
			c := cond{Path: m.condPath, All: all, Text: "select(" + condExpr(m.condPath, text, all) + ")"}
			if _, err := compile(c.Text); err != nil {
				m.msg = "Invalid condition: " + err.Error()
				return m, nil
			}
			m.inputKind = inputNone
			nb := m.b.clone()
			nb.Conds = append(nb.Conds, c)
			return m, m.setBuilder(nb)
		case inputRaw:
			if _, err := compile(text); err != nil {
				m.msg = "Invalid filter: " + err.Error()
				return m, nil
			}
			m.inputKind = inputNone
			if text == m.b.Filter() {
				return m, m.startEval(text)
			}
			return m, m.setBuilder(builder{IsRaw: true, Raw: text})
		}
	}
	var cmd tea.Cmd
	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.inputKind == inputRaw && m.input.Value() != before {
		// Live preview while typing.
		return m, tea.Batch(cmd, m.startEval(m.input.Value()))
	}
	return m, cmd
}

func (m *model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		m.printOnExit = true
		return m, tea.Quit
	case "e":
		m.inputKind = inputRaw
		m.input.SetValue(m.b.Filter())
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink
	case "u":
		if n := len(m.undo); n > 0 {
			m.b = m.undo[n-1]
			m.undo = m.undo[:n-1]
			m.msg = "Undone."
			return m, m.startEval(m.b.Filter())
		}
		m.msg = "Nothing to undo."
		return m, nil
	case "X":
		m.msg = "Filter cleared (u to undo)."
		return m, m.setBuilder(builder{})
	case "a":
		m.allMode = !m.allMode
		if m.allMode {
			m.msg = "Arrays: any element ([])."
		} else {
			m.msg = "Arrays: exact index ([0])."
		}
		return m, nil
	}
	if m.mode == modeTree {
		return m.updateTree(msg)
	}
	return m.updateList(msg)
}

func (m *model) mainHeight() int {
	return max(1, m.height-len(m.topLines())-2)
}

func (m *model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.mainHeight()
	switch msg.String() {
	case "up", "k":
		m.listCur--
	case "down", "j":
		m.listCur++
	case "pgup", "ctrl+b":
		m.listCur -= h
	case "pgdown", "ctrl+f", " ":
		m.listCur += h
	case "home", "g":
		m.listCur = 0
	case "end", "G":
		m.listCur = len(m.matched) - 1
	case "enter", "right", "l":
		if len(m.matched) > 0 {
			m.openTree(m.matched[m.listCur])
		}
	}
	m.listCur = clamp(m.listCur, 0, len(m.matched)-1)
	return m, nil
}

func (m *model) openTree(i int) {
	n, err := parseNode(m.items[i].Raw)
	if err != nil {
		m.msg = "Error: " + err.Error()
		return
	}
	m.mode = modeTree
	m.treeItem = i
	m.tree = n
	m.collapsed = map[string]bool{}
	m.treeCur, m.treeOff = 0, 0
	m.rebuildLines()
}

func (m *model) rebuildLines() {
	m.lines = m.lines[:0]
	flatten(m.tree, nil, 0, m.collapsed, &m.lines)
	m.treeCur = clamp(m.treeCur, 0, len(m.lines)-1)
}

func flatten(n *Node, path []Seg, depth int, collapsed map[string]bool, out *[]treeLine) {
	for i, child := range n.Children {
		p := append(append([]Seg(nil), path...), Seg{Index: i, IsIndex: n.Kind == KArray})
		label := fmt.Sprintf("[%d]", i)
		if n.Kind == KObject {
			p[len(p)-1].Key = n.Keys[i]
			label = n.Keys[i]
		}
		*out = append(*out, treeLine{Path: p, Depth: depth, Label: label, Node: child})
		if child.IsContainer() && !collapsed[formatPath(p, false)] {
			flatten(child, p, depth+1, collapsed, out)
		}
	}
}

func (m *model) updateTree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.mainHeight()
	var line *treeLine
	if len(m.lines) > 0 {
		line = &m.lines[m.treeCur]
	}
	switch msg.String() {
	case "esc", "backspace":
		m.mode = modeList
		return m, nil
	case "up", "k":
		m.treeCur--
	case "down", "j":
		m.treeCur++
	case "pgup", "ctrl+b":
		m.treeCur -= h
	case "pgdown", "ctrl+f":
		m.treeCur += h
	case "home", "g":
		m.treeCur = 0
	case "end", "G":
		m.treeCur = len(m.lines) - 1
	case "left", "h":
		if line == nil {
			break
		}
		key := formatPath(line.Path, false)
		if line.Node.IsContainer() && !m.collapsed[key] {
			m.collapsed[key] = true
			m.rebuildLines()
		} else {
			for i := m.treeCur - 1; i >= 0; i-- {
				if m.lines[i].Depth < line.Depth {
					m.treeCur = i
					break
				}
			}
		}
	case "right", "l", " ":
		if line != nil && line.Node.IsContainer() {
			key := formatPath(line.Path, false)
			m.collapsed[key] = msg.String() == " " && !m.collapsed[key]
			m.rebuildLines()
		}
	case "]", "[":
		m.jumpItem(msg.String() == "]")
	case "f":
		if line == nil {
			break
		}
		if m.b.IsRaw {
			m.msg = "Filter edited by hand: u to undo, X to start over."
			break
		}
		m.condPath = line.Path
		m.condAll = m.allMode
		test := "!= null"
		if !line.Node.IsContainer() {
			test = "== " + line.Node.Raw
		}
		m.inputKind = inputCond
		m.input.SetValue(test)
		m.input.CursorEnd()
		m.input.Focus()
		return m, textinput.Blink
	case "p":
		if line == nil {
			break
		}
		if m.b.IsRaw {
			m.msg = "Filter edited by hand: u to undo, X to start over."
			break
		}
		return m, m.toggleProj(line.Path)
	}
	m.treeCur = clamp(m.treeCur, 0, len(m.lines)-1)
	return m, nil
}

func (m *model) toggleProj(path []Seg) tea.Cmd {
	all := m.allMode && hasIndex(path)
	key := formatPath(path, all)
	nb := m.b.clone()
	for i, p := range nb.Projs {
		if formatPath(p.Path, p.All) == key {
			nb.Projs = append(nb.Projs[:i], nb.Projs[i+1:]...)
			m.msg = "Projection removed: " + projExpr(p)
			return m.setBuilder(nb)
		}
	}
	p := proj{Path: path, All: all}
	nb.Projs = append(nb.Projs, p)
	m.msg = "Projection added: " + projExpr(p)
	return m.setBuilder(nb)
}

// jumpItem moves to the next or previous kept value without leaving the tree.
func (m *model) jumpItem(next bool) {
	for k, idx := range m.matched {
		if next && idx > m.treeItem {
			m.listCur = k
			m.keepTreeState(idx)
			return
		}
		if !next && idx >= m.treeItem {
			if k > 0 {
				m.listCur = k - 1
				m.keepTreeState(m.matched[k-1])
			}
			return
		}
	}
	if !next && len(m.matched) > 0 {
		m.listCur = len(m.matched) - 1
		m.keepTreeState(m.matched[m.listCur])
	}
}

// keepTreeState opens another value, keeping collapsed nodes and position.
func (m *model) keepTreeState(i int) {
	collapsed, cur, off := m.collapsed, m.treeCur, m.treeOff
	m.openTree(i)
	m.collapsed, m.treeCur, m.treeOff = collapsed, cur, off
	m.rebuildLines()
}

// marker tells whether a tree line carries a condition or a projection.
func (m *model) marker(p []Seg) string {
	for _, pr := range m.b.Projs {
		if formatPath(pr.Path, pr.All) == formatPath(p, pr.All) {
			return stProjMk.Render("◆")
		}
	}
	for _, c := range m.b.Conds {
		if formatPath(c.Path, c.All) == formatPath(p, c.All) {
			return stCondMk.Render("●")
		}
	}
	return " "
}

// ---- Rendering ----

func (m *model) topLines() []string {
	kept := fmt.Sprintf("%d kept", len(m.matched))
	if m.evaluating {
		kept = "evaluating…"
	}
	head := fmt.Sprintf(" jqe · %s · %d values · %s", m.file, len(m.items), kept)
	if m.allMode {
		head += " · arrays: []"
	} else {
		head += " · arrays: [n]"
	}
	if m.b.IsRaw {
		head += " · edited by hand"
	}
	lines := []string{stHeader.Render(padRight(head, m.width))}
	if m.inputKind == inputRaw {
		lines = append(lines, "jq> "+m.input.View())
	} else {
		wrapped := strings.Split(ansi.Hardwrap("jq> "+m.b.Filter(), m.width, false), "\n")
		if len(wrapped) > 4 {
			wrapped = append(wrapped[:3], "…")
		}
		for _, l := range wrapped {
			lines = append(lines, stFilter.Render(l))
		}
	}
	if m.compileErr != "" {
		lines = append(lines, stErr.Render(ansi.Truncate(m.compileErr, m.width, "…")))
	}
	return lines
}

func (m *model) View() string {
	var sb strings.Builder
	for _, l := range m.topLines() {
		sb.WriteString(l + "\n")
	}
	h := m.mainHeight()
	var body []string
	if m.mode == modeTree {
		body = m.treeView(h)
	} else {
		body = m.listView(h)
	}
	for len(body) < h {
		body = append(body, "")
	}
	sb.WriteString(strings.Join(body, "\n") + "\n")
	sb.WriteString(m.statusLine() + "\n")
	sb.WriteString(stDim.Render(ansi.Truncate(m.helpLine(), m.width, "…")))
	return sb.String()
}

func (m *model) listView(h int) []string {
	if len(m.matched) == 0 {
		if m.evaluating {
			return []string{stDim.Render("  evaluating…")}
		}
		return []string{stDim.Render("  no value kept by the filter")}
	}
	m.listOff = scroll(m.listCur, m.listOff, h)
	var out []string
	numW := len(fmt.Sprint(len(m.items)))
	for k := m.listOff; k < len(m.matched) && k < m.listOff+h; k++ {
		i := m.matched[k]
		r := m.results[i]
		num := fmt.Sprintf("%*d ", numW, i+1)
		var text string
		if r.Err != "" {
			text = "error: " + r.Err
		} else {
			text = strings.Join(r.Outs, "  ")
			if r.More {
				text += "  …"
			}
		}
		line := ansi.Truncate(text, max(1, m.width-len(num)), "…")
		if k == m.listCur {
			out = append(out, stCursor.Render(num+padRight(line, m.width-len(num))))
		} else if r.Err != "" {
			out = append(out, stDim.Render(num)+stErr.Render(line))
		} else {
			out = append(out, stDim.Render(num)+line)
		}
	}
	return out
}

func (m *model) treeView(h int) []string {
	m.treeOff = scroll(m.treeCur, m.treeOff, h)
	var out []string
	for k := m.treeOff; k < len(m.lines) && k < m.treeOff+h; k++ {
		l := m.lines[k]
		indent := strings.Repeat("  ", l.Depth)
		var value, plain string
		n := l.Node
		switch {
		case n.IsContainer():
			open, close := "{", "}"
			if n.Kind == KArray {
				open, close = "[", "]"
			}
			arrow := "▾ "
			if m.collapsed[formatPath(l.Path, false)] {
				arrow = "▸ "
			}
			indent += arrow
			plain = fmt.Sprintf("%s%d%s", open, len(n.Children), close)
			value = stDim.Render(plain)
		default:
			indent += "  "
			plain = n.Raw
			st := stNull
			switch n.Kind {
			case KString:
				st = stString
			case KNumber, KBool:
				st = stNumber
			}
			avail := max(1, m.width-2-len(indent)-ansi.StringWidth(l.Label)-2)
			plain = ansi.Truncate(plain, avail, "…")
			value = st.Render(plain)
		}
		mk := m.marker(l.Path)
		if k == m.treeCur {
			text := padRight(indent+l.Label+": "+plain, m.width-2)
			out = append(out, mk+" "+stCursor.Render(text))
		} else {
			out = append(out, mk+" "+indent+stKey.Render(l.Label)+": "+value)
		}
	}
	return out
}

func (m *model) statusLine() string {
	if m.inputKind == inputCond {
		mode := "index"
		if m.condAll {
			mode = "any element"
		}
		prefix := formatPath(m.condPath, false) + " "
		// A long path is shortened from the left to leave room for the input.
		if over := ansi.StringWidth(prefix) - max(20, m.width/2); over > 0 {
			prefix = "…" + ansi.TruncateLeft(prefix, over+1, "")
		}
		if hasIndex(m.condPath) {
			prefix = "[" + mode + ", ctrl+a] " + prefix
		}
		prefix = stCondMk.Render("condition ") + prefix
		m.input.Width = max(10, m.width-ansi.StringWidth(prefix)-1)
		return prefix + m.input.View()
	}
	if m.msg != "" {
		return stMessage.Render(ansi.Truncate(m.msg, m.width, "…"))
	}
	if m.mode == modeTree && len(m.lines) > 0 {
		return stDim.Render(fmt.Sprintf("value %d · %s", m.treeItem+1, formatPath(m.lines[m.treeCur].Path, false)))
	}
	return ""
}

func (m *model) helpLine() string {
	switch {
	case m.inputKind == inputCond:
		return "enter: add · esc: cancel · e.g. == \"x\"  != null  > 5  | test(\"^02\")"
	case m.inputKind == inputRaw:
		return "enter: apply · esc: cancel · live preview"
	case m.mode == modeTree:
		return "f: filter · p: project · ←/→: collapse/expand · [ ]: prev/next value · esc: list · e: edit · u: undo · a: arrays · q: quit"
	default:
		return "enter: expand · e: edit filter · u: undo · X: clear all · a: arrays · q: quit and print filter · ctrl+c: abort"
	}
}

// ---- Helpers ----

func clamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

// scroll adjusts the offset so the cursor stays visible.
func scroll(cur, off, h int) int {
	if cur < off {
		off = cur
	}
	if cur >= off+h {
		off = cur - h + 1
	}
	return max(0, off)
}

func padRight(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return ansi.Truncate(s, w, "")
}
