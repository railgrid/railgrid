/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// errPickerCancelled is returned by runPicker when the user aborts the
// selection (esc / ctrl-c) instead of choosing an item.
var errPickerCancelled = errors.New("selection cancelled")

// pickerItem is one selectable row. title is the primary label; desc is a
// dimmed secondary line (UUID, badges, …) shown beneath it.
type pickerItem struct {
	title string
	desc  string
}

const pickerMaxVisible = 10

var (
	pickerTitleStyle    = lipgloss.NewStyle().Bold(true)
	pickerCursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	pickerSelTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	pickerDescStyle     = lipgloss.NewStyle().Faint(true)
	pickerHelpStyle     = lipgloss.NewStyle().Faint(true)
)

type pickerModel struct {
	title    string
	all      []pickerItem
	filtered []int // indices into all that match the current filter
	cursor   int   // index into filtered
	filter   string
	chosen   int // index into all; -1 until the user presses enter
	quitting bool
}

func (m *pickerModel) applyFilter() {
	m.filtered = m.filtered[:0]
	q := strings.ToLower(strings.TrimSpace(m.filter))
	for i, it := range m.all {
		if q == "" || strings.Contains(strings.ToLower(it.title), q) || strings.Contains(strings.ToLower(it.desc), q) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.chosen = -1
		m.quitting = true
		return m, tea.Quit
	case tea.KeyEnter:
		if len(m.filtered) > 0 {
			m.chosen = m.filtered[m.cursor]
		}
		m.quitting = true
		return m, tea.Quit
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyDown:
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
	case tea.KeyBackspace:
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.applyFilter()
		}
	case tea.KeySpace:
		m.filter += " "
		m.applyFilter()
	case tea.KeyRunes:
		m.filter += string(key.Runes)
		m.applyFilter()
	}
	return m, nil
}

func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString(pickerTitleStyle.Render(m.title))
	if m.filter != "" {
		b.WriteString(pickerDescStyle.Render("  filter: " + m.filter))
	}
	b.WriteString("\n\n")

	if len(m.filtered) == 0 {
		b.WriteString(pickerDescStyle.Render("  (no matches)"))
		b.WriteString("\n")
	}

	start := 0
	if m.cursor >= pickerMaxVisible {
		start = m.cursor - pickerMaxVisible + 1
	}
	end := min(start+pickerMaxVisible, len(m.filtered))
	for i := start; i < end; i++ {
		it := m.all[m.filtered[i]]
		if i == m.cursor {
			b.WriteString(pickerCursorStyle.Render("› "))
			b.WriteString(pickerSelTitleStyle.Render(it.title))
		} else {
			b.WriteString("  ")
			b.WriteString(it.title)
		}
		if it.desc != "" {
			b.WriteString(pickerDescStyle.Render("  " + it.desc))
		}
		b.WriteString("\n")
	}
	if len(m.filtered) > pickerMaxVisible {
		b.WriteString(pickerDescStyle.Render(fmt.Sprintf("  …showing %d-%d of %d", start+1, end, len(m.filtered))))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(pickerHelpStyle.Render("↑/↓ navigate · enter select · type to filter · esc cancel"))
	b.WriteString("\n")
	return b.String()
}

// runPicker renders an interactive single-select list and returns the index
// (into items) of the chosen entry, or errPickerCancelled if the user aborts.
// The UI is drawn on stderr so stdout stays clean for scripted callers.
func runPicker(title string, items []pickerItem) (int, error) {
	m := pickerModel{title: title, all: items, chosen: -1}
	m.applyFilter()
	prog := tea.NewProgram(m, tea.WithInput(os.Stdin), tea.WithOutput(os.Stderr))
	res, err := prog.Run()
	if err != nil {
		return -1, fmt.Errorf("running selector: %w", err)
	}
	final := res.(pickerModel)
	if final.chosen < 0 {
		return -1, errPickerCancelled
	}
	return final.chosen, nil
}
