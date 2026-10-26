package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	red     = lipgloss.Color("1")
	green   = lipgloss.Color("2")
	yellow  = lipgloss.Color("3")
	magenta = lipgloss.Color("5")
	gray    = lipgloss.Color("8")

	headerStyle   = lipgloss.NewStyle().Bold(true).Reverse(true)
	warningStyle  = lipgloss.NewStyle().Foreground(yellow)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	stderrStyle   = lipgloss.NewStyle().Foreground(red)
	deniedStyle   = lipgloss.NewStyle().Foreground(magenta)
	sepStyle      = lipgloss.NewStyle().Foreground(gray)
)

// maxWarnings is how many warning lines the header shows.
const maxWarnings = 3

func (m *Model) headerHeight() int {
	n := len(m.cfgWarnings)
	if n > maxWarnings {
		n = maxWarnings + 1 // "+N more"
	}
	if m.warning != nil {
		n++
	}
	return 1 + n
}

func (m *Model) bodyHeight() int { return m.height - m.headerHeight() - 1 }

// listRows is the height of the run list above the detail pane.
const listRows = 10

// detailHeader is the number of lines above the output in the detail pane.
const detailHeader = 3

// minDetail is the detail height the list gives way to in short windows.
const minDetail = detailHeader + 3

// listHeight is the height of the run list; the separator line follows it.
// The list shrinks in short windows and to make room for a pending config change.
func (m *Model) listHeight() int {
	detail := minDetail
	if m.change != nil {
		detail = max(detail, detailHeader+len(m.change.Diff)+2)
	}
	return max(1, min(listRows, m.bodyHeight()-1-detail))
}

func (m *Model) detailHeight() int { return max(0, m.bodyHeight()-m.listHeight()-1) }

// layout sizes the viewport after the window or the header changed.
func (m *Model) layout() {
	m.vp.SetWidth(m.width)
	m.vp.SetHeight(max(0, m.detailHeight()-detailHeader))
	m.scrollList()
	if m.vpRun != nil {
		m.vp.SetContentLines(renderLines(m.vpRun))
	}
}

// View implements tea.Model.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "hoex host"
	if m.change != nil {
		v.WindowTitle = "hoex host · config change waiting for approval"
	}
	return v
}

func (m Model) render() string {
	if m.width < 40 || m.height < 8 {
		return "hoex host: window too small"
	}
	var b strings.Builder
	b.WriteString(fit(headerStyle.Render(pad(" "+m.title(), m.width)), m.width))
	b.WriteByte('\n')
	for i, w := range m.cfgWarnings {
		if i == maxWarnings {
			b.WriteString(warningStyle.Render(fmt.Sprintf("⚠ +%d more warnings", len(m.cfgWarnings)-maxWarnings)))
			b.WriteByte('\n')
			break
		}
		b.WriteString(fit(warningStyle.Render("⚠ "+m.relative(w)), m.width))
		b.WriteByte('\n')
	}
	if w := m.warning; w != nil {
		b.WriteString(fit(warningStyle.Render(w.Time.Format("15:04:05")+" ⚠ "+m.relative(w.Text)), m.width))
		b.WriteByte('\n')
	}

	for _, l := range m.renderList() {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(sepStyle.Render(strings.Repeat("─", m.width)))
	b.WriteByte('\n')
	for _, l := range m.renderDetail() {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	help := " j/k select · h/l scroll row · J output · f follow latest · q quit"
	if m.focus == outputPane {
		help = " j/k scroll · h/l scroll sideways · K run list · f follow latest · q quit"
	}
	if m.change != nil {
		help = " a approve config change · r reject · j/k select · q quit"
	}
	b.WriteString(fit(faintStyle.Render(help), m.width))
	return b.String()
}

func (m Model) title() string {
	if m.host == nil {
		if m.stopping {
			return "hoex host · shutting down…"
		}
		return "hoex host · starting…"
	}
	active := 0
	for _, r := range m.runs {
		if r.state == running {
			active++
		}
	}
	t := fmt.Sprintf("hoex host · %s · port %d · %d rules · %d runs", m.home(m.host.Root), m.host.Port, m.host.Rules, len(m.runs))
	if active > 0 {
		t += fmt.Sprintf(" (%d running)", active)
	}
	if m.stopping {
		t += " · shutting down…"
	}
	return t
}

// renderList returns exactly listHeight full-width lines.
func (m Model) renderList() []string {
	w, h := m.width, m.listHeight()
	lines := make([]string, h)
	for i := range h {
		idx := m.listTop + i
		if idx >= len(m.runs) {
			lines[i] = pad("", w)
			continue
		}
		r := m.runs[idx]
		_, style := status(r)
		text := listText(r)
		if idx == m.sel && m.listX > 0 {
			// The ellipsis takes the place of the last cell cut off.
			text = ansi.TruncateLeft(text, m.listX+1, "…")
		}
		text = fit(text, w)
		switch {
		case idx != m.sel:
			lines[i] = style.Render(text)
		case m.focus == listPane:
			lines[i] = selectedStyle.Render(text)
		default:
			lines[i] = style.Bold(true).Underline(true).Render(text)
		}
	}
	return lines
}

// listText is the row of a run in the list.
func listText(r *run) string {
	icon, _ := status(r)
	return fmt.Sprintf("%s #%d %s %s", icon, r.id, r.start.Format("15:04:05"), r.command())
}

// status returns the list icon and color of a run.
func status(r *run) (string, lipgloss.Style) {
	switch r.state {
	case running:
		return "●", lipgloss.NewStyle().Foreground(yellow)
	case denied:
		return "⊘", deniedStyle
	case aborted:
		return "■", lipgloss.NewStyle().Foreground(gray)
	}
	if r.exitCode == 0 {
		return "✓", lipgloss.NewStyle().Foreground(green)
	}
	return "✗", lipgloss.NewStyle().Foreground(red)
}

// renderDetail returns exactly detailHeight full-width lines.
func (m Model) renderDetail() []string {
	w, h := m.width, m.detailHeight()
	lines := make([]string, 0, h)
	add := func(s string) { lines = append(lines, fit(s, w)) }

	r := m.selected()
	if m.change != nil {
		lines = m.changeLines(w)
	} else if r == nil {
		add("")
		add(" Waiting for commands from the sandbox…")
		add("")
		add(faintStyle.Render(" In the sandbox: hoex --mode sandbox -- <command>"))
	} else {
		title := boldStyle
		if m.focus == outputPane {
			title = selectedStyle.Bold(true)
		}
		add(title.Render(pad(fmt.Sprintf(" #%d  %s", r.id, r.command()), w)))
		add(" " + m.summary(r))
		add(sepStyle.Render(strings.Repeat("─", w)))
		if r.state == denied {
			add(deniedStyle.Render(" Rejected: " + r.reason))
		} else {
			for _, l := range strings.Split(m.vp.View(), "\n") {
				add(l)
			}
		}
	}
	for len(lines) < h {
		add("")
	}
	return lines[:h]
}

// changeLines shows the config change waiting for approval.
func (m Model) changeLines(w int) []string {
	c := m.change
	lines := []string{
		fit(warningStyle.Bold(true).Render(" ⚠ "+m.relative(c.File)+" changed"), w),
		fit(" The new rules are not active until you approve them.", w),
		sepStyle.Render(strings.Repeat("─", w)),
	}
	room := m.detailHeight() - len(lines) - 2
	for i, d := range c.Diff {
		if i == room-1 && len(c.Diff) > room {
			lines = append(lines, fit(faintStyle.Render(fmt.Sprintf(" … %d more", len(c.Diff)-i)), w))
			break
		}
		style := warningStyle
		switch d[0] {
		case '+':
			style = lipgloss.NewStyle().Foreground(green)
		case '-':
			style = stderrStyle
		}
		lines = append(lines, fit(style.Render(" "+d), w))
	}
	lines = append(lines, fit("", w), fit(boldStyle.Render(" a approve · r reject"), w))
	return lines
}

// summary is the status line of a run: directory, start, duration, result.
func (m Model) summary(r *run) string {
	var parts []string
	if r.cwd != "" {
		parts = append(parts, faintStyle.Render("in "+m.relDir(r.cwd)))
	}
	parts = append(parts, faintStyle.Render(r.start.Format("15:04:05")))
	end := r.end
	if r.state == running {
		end = m.now()
	}
	if r.state != denied {
		parts = append(parts, faintStyle.Render(end.Sub(r.start).Round(100*time.Millisecond).String()))
	}
	_, style := status(r)
	switch r.state {
	case running:
		parts = append(parts, style.Render("running"))
	case exited:
		parts = append(parts, style.Render(fmt.Sprintf("exit %d", r.exitCode)))
	case aborted:
		parts = append(parts, style.Render("aborted: "+r.reason))
	case denied:
		parts = append(parts, style.Render("denied"))
	}
	if r.dropped > 0 {
		parts = append(parts, faintStyle.Render(fmt.Sprintf("first %d lines not kept", r.dropped)))
	}
	return strings.Join(parts, faintStyle.Render(" · "))
}

// home abbreviates the home directory as ~.
func (m Model) home(path string) string {
	if m.homeDir != "" && (path == m.homeDir || strings.HasPrefix(path, m.homeDir+"/")) {
		return "~" + path[len(m.homeDir):]
	}
	return path
}

// relDir shows a directory relative to the project root.
func (m Model) relDir(dir string) string {
	if m.host == nil {
		return m.home(dir)
	}
	if rel, err := filepath.Rel(m.host.Root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return m.home(dir)
}

// relative drops the project root from paths in a message.
func (m Model) relative(msg string) string {
	if m.host == nil {
		return msg
	}
	return strings.ReplaceAll(msg, m.host.Root+"/", "")
}

// renderLines styles the output of a run for the viewport.
func renderLines(r *run) []string {
	out := make([]string, 0, len(r.lines)+2)
	for _, l := range r.lines {
		if l.stderr {
			out = append(out, stderrStyle.Render(l.text))
		} else {
			out = append(out, l.text)
		}
	}
	for i, p := range r.partial {
		if len(p) > 0 {
			text := sanitize(string(p))
			if i == 1 {
				text = stderrStyle.Render(text)
			}
			out = append(out, text)
		}
	}
	return out
}

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	return pad(ansi.Truncate(s, w, "…"), w)
}

func pad(s string, w int) string {
	if n := w - ansi.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
