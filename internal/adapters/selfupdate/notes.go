package selfupdate

import (
	"strings"

	"github.com/rupaai/homelab/internal/core"
)

// ParseNotes reads a release's notes, as `homelab-services release` writes them: the
// headings "## Breaking", "## New" and "## Fixes", each with lines starting "- ". It counts
// those lines and keeps the notes without the install hint that follows them. It returns
// nil when the notes have no such lines (a release made by hand).
func ParseNotes(body string) *core.UpdateChanges {
	var c core.UpdateChanges
	var kept []string
	var count *int
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if h, ok := strings.CutPrefix(line, "## "); ok {
			switch strings.TrimSpace(h) {
			case "Breaking":
				count = &c.Breaking
			case "New":
				count = &c.New
			case "Fixes":
				count = &c.Fixes
			default:
				count = nil
			}
			if count != nil {
				kept = append(kept, line)
			}
			continue
		}
		if count == nil {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			*count++
			kept = append(kept, line)
		} else if line == "" {
			kept = append(kept, line)
		} else {
			// Text that isn't a note line ends the section (the install hint).
			count = nil
		}
	}
	if c.Breaking+c.New+c.Fixes == 0 {
		return nil
	}
	c.Notes = strings.TrimSpace(strings.Join(kept, "\n"))
	return &c
}
