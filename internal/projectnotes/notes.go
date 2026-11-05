// Package projectnotes reads the attributed project claims kept in a repository.
// Acceptance is a workflow fact and is never parsed from this file.
package projectnotes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const Path = "PROJECT-NOTES.md"
const maxBytes = 6144
const maxNotes = 40

type Note struct {
	Text   string `json:"text"`
	Author string `json:"author"`
	Change int64  `json:"change"`
	Line   string `json:"line"`
}

var noteLine = regexp.MustCompile(`^- (.+\.) \(([^()]+) · change ([1-9][0-9]*)\)$`)

// parse accepts a missing (nil) file. Errors name the offending line so a
// malformed landed file remains visibly unreadable rather than an empty list.
func parse(body []byte) ([]Note, error) {
	out := []Note{}
	if body == nil {
		return out, nil
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("%s:1: exceeds %d bytes", Path, maxBytes)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if lines[0] != "# Project notes" {
		return nil, fmt.Errorf("%s:1: expected # Project notes", Path)
	}
	for i, line := range lines[1:] {
		if line == "" {
			continue
		}
		m := noteLine.FindStringSubmatch(line)
		if m == nil || strings.Trim(m[1], " \t\r\v\f") == "." || strings.Trim(m[2], " \t\r\v\f") != m[2] {
			return nil, fmt.Errorf("%s:%d: expected - One sentence. (author · change N)", Path, i+2)
		}
		n, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: invalid change number", Path, i+2)
		}
		if len(out) == maxNotes {
			return nil, fmt.Errorf("%s:%d: exceeds %d notes", Path, i+2, maxNotes)
		}
		out = append(out, Note{Text: m[1], Author: m[2], Change: n, Line: line})
	}
	return out, nil
}

type Diff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// FromDiff keeps the actual added and removed lines, including invalid notes:
// acceptance must show what is proposed even when its format check fails.
func FromDiff(raw string) *Diff {
	if raw == "" {
		return nil
	}
	d := &Diff{Added: []string{}, Removed: []string{}}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") {
			d.Added = append(d.Added, line[1:])
		}
		if strings.HasPrefix(line, "-") {
			d.Removed = append(d.Removed, line[1:])
		}
	}
	return d
}
