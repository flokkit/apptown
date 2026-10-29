package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/flokkit/apptown/internal/strs"
)

var isTTY = func() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func color(code, s string) string {
	if !isTTY {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func dim(s string) string { return color("2", s) }

func green(s string) string { return color("32", s) }

func red(s string) string { return color("31", s) }

func yellow(s string) string { return color("33", s) }

func bold(s string) string { return color("1", s) }

func cyan(s string) string { return color("36", s) }

var stdin = bufio.NewReader(os.Stdin)

func ask(prompt string) string {
	fmt.Print(prompt)
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

func truncateStr(s string, n int) string {
	return strs.Truncate(strings.TrimRight(s, "\n"), n)
}

func indentBody(s string, maxLines int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], dim(fmt.Sprintf("… (%d more lines)", len(lines)-maxLines)))
	}
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// stateChip is a change's state label (AppTown's words) marked by its
// state: closed and shipped, closed otherwise, landing, or in progress.
func stateChip(state, label string) string {
	switch state {
	case "shipped":
		return green("● " + label)
	case "abandoned":
		return dim("○ " + label)
	case "landing":
		return yellow("◐ " + label)
	default:
		return cyan("◉ " + label)
	}
}

func tw() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
}

func fmtMs(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func fmtMicros(m int64) string {
	if m <= 0 {
		return "-"
	}
	return fmt.Sprintf("$%.2f", float64(m)/1e6)
}
