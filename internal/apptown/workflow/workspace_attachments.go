package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/flokkit/apptown/internal/ids"
)

// SyncAttachments copies canonical human attachments into the existing workspace.
// Missing canonical attachments are normal; other read/write failures are reported.
func (g GitWorkspace) SyncAttachments(ctx context.Context, changeID ids.ID, workspace string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if changeID.IsZero() {
		return fmt.Errorf("attachments need a Change")
	}
	src := g.AttachmentDir(changeID)
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	info, err := os.Stat(filepath.Join(workspace, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("attachment workspace must be standalone")
	}
	dst := filepath.Join(workspace, ".apptown", "attachments")
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (g GitWorkspace) AttachmentDir(changeID ids.ID) string {
	return filepath.Join(g.DataDir, "changes", fmt.Sprintf("%d", changeID), "attachments")
}

// Attachments distinguishes an absent directory from unavailable contents.
func (g GitWorkspace) Attachments(changeID ids.ID) ([]Attachment, error) {
	entries, err := os.ReadDir(g.AttachmentDir(changeID))
	if errors.Is(err, os.ErrNotExist) {
		return []Attachment{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Attachment{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(g.AttachmentDir(changeID), e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Attachment{Name: e.Name(), Bytes: info.Size()})
	}
	return out, nil
}

func (g GitWorkspace) AttachmentNames(changeID ids.ID) ([]string, error) {
	attachments, err := g.Attachments(changeID)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, attachment := range attachments {
		names = append(names, attachment.Name)
	}
	sort.Strings(names)
	return names, nil
}
