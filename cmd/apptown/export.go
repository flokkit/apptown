package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"flokkit.io/forge/internal/ids"

	"golang.org/x/term"
)

// cmdExportProject: apptown export-project -p <slug> > file — an
// administrator's copy of a project, with its history and chats, for moving
// it to another AppTown.
// The file goes to standard output; the hash, counts and the sensitivity
// warning go to standard error.
func cmdExportProject(args []string) error {
	fs := newFlags("export-project")
	project := fs.String("p", "", "project slug")
	parseArgs(fs, args)
	if *project == "" {
		return usagef("usage: apptown export-project -p <project> > <file>")
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return usagef("the export is a file: send it somewhere, e.g. apptown export-project -p %s > %s.apptown-export", *project, *project)
	}
	return exportProject(*project, os.Stdout, os.Stderr)
}

func exportProject(slug string, out, report io.Writer) error {
	path := "/projects/" + slug + "/export"
	res, err := api.send("GET", path, nil)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		_, err := api.answer(res, "GET", path)
		return err
	}
	defer res.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), res.Body)
	if err != nil {
		return fmt.Errorf("the export broke off after %d bytes: %w", n, err)
	}
	got, want := hex.EncodeToString(h.Sum(nil)), res.Header.Get("X-AppTown-Export-Sha256")
	if got != want {
		return fmt.Errorf("the file that arrived (sha256 %s) is not the one AppTown made (%s): export again", got, want)
	}
	var counts struct {
		Counts             map[string]int `json:"counts"`
		EmptyChats         int            `json:"emptyChats"`
		MissingTranscripts []string       `json:"missingTranscripts"`
		Skipped            []struct {
			Kind  string `json:"kind"`
			ID    ids.ID `json:"id"`
			Title string `json:"title"`
			State string `json:"state"`
		} `json:"skipped"`
		SkippedChats int `json:"skippedChats"`
	}
	_ = json.Unmarshal([]byte(res.Header.Get("X-AppTown-Export-Counts")), &counts)
	fmt.Fprintf(report, "%s exported %s: %d bytes, sha256 %s\n", green("✓"), slug, n, got)
	fmt.Fprintf(report, "  changes %d · runs %d · steps %d · chats %d (%d empty, not moved) · refs %d · ledger lines %d\n",
		counts.Counts["changes"], counts.Counts["runs"], counts.Counts["steps"], counts.Counts["chats"], counts.EmptyChats,
		counts.Counts["refs"], counts.Counts["ledger"])
	if len(counts.Skipped) > 0 || counts.SkippedChats > 0 {
		fmt.Fprintf(report, "  stays on this AppTown (only landed history moves; the file lists it): %d chats live or on a change not landed\n", counts.SkippedChats)
		for _, s := range counts.Skipped {
			fmt.Fprintf(report, "    %s %d (%s): %s\n", s.Kind, s.ID, s.State, s.Title)
		}
	}
	for _, m := range counts.MissingTranscripts {
		fmt.Fprintf(report, "  a step transcript this AppTown no longer holds: %s\n", m)
	}
	fmt.Fprintln(report, "  this file is as sensitive as AppTown's database: logs, chats and artifacts are in it as they are")
	fmt.Fprintln(report, "  only an AppTown running this same build reads it: the export is experimental, not a format kept across releases")
	return nil
}
