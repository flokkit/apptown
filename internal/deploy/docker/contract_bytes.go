package docker

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/gitexec"
)

// ContractPath is where the app contract lives in a repository.
const ContractPath = ".apptown/app.yaml"

// MaxContractBytes bounds the app contract read from a commit.
const MaxContractBytes = 1 << 20

// ContractFileAt is the app contract at an exact revision: the file at
// ContractPath, unparsed. It is every reader's: deploys, previews, an
// agent's needs, the MCP read and a change's additions all read a commit's
// contract here, so they agree on what a commit declares.
//
// No contract is ErrNoContract, but only when .apptown is missing or a real
// folder: a link where the folder should be, a contract that is not a
// regular file, or one over MaxContractBytes is an error, never "nothing".
// The reads are git plumbing run hardened (gitexec.HubOutput).
func ContractFileAt(ctx context.Context, repo, sha string) ([]byte, error) {
	commit, err := (deploy.SelectedSource{Repository: repo, Revision: sha}).Resolve(ctx)
	if err != nil {
		return nil, err
	}
	entry := func(path string) (mode, kind, object string, err error) {
		out, err := gitexec.HubOutput(ctx, repo, nil, "ls-tree", "-z", "--full-tree", commit, "--", path)
		if err != nil {
			return "", "", "", fmt.Errorf("listing %s at %s: %w", path, commit, err)
		}
		if len(out) == 0 {
			return "", "", "", nil
		}
		meta, _, ok := bytes.Cut(bytes.TrimSuffix(out, []byte{0}), []byte("\t"))
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 3 {
			return "", "", "", fmt.Errorf("%s at %s: unexpected entry %q", path, commit, meta)
		}
		return fields[0], fields[1], fields[2], nil
	}
	mode, kind, object, err := entry(ContractPath)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		// Git lists nothing under a folder that is a link: only a missing
		// or real .apptown folder means there is no contract.
		folderMode, folder, _, err := entry(".apptown")
		if err != nil {
			return nil, err
		}
		if folderMode == "120000" {
			return nil, fmt.Errorf(".apptown at %s is a link, which AppTown does not follow", commit)
		}
		if folder != "" && folder != "tree" {
			return nil, fmt.Errorf(".apptown at %s is not a folder", commit)
		}
		return nil, ErrNoContract
	}
	if mode == "120000" {
		return nil, fmt.Errorf("%s at %s is a link, which AppTown does not follow", ContractPath, commit)
	}
	if kind != "blob" || (mode != "100644" && mode != "100755") {
		return nil, fmt.Errorf("%s at %s is not a regular file (%s %s)", ContractPath, commit, mode, kind)
	}
	// The size first, so an oversized contract is never read into memory.
	out, err := gitexec.HubOutput(ctx, repo, nil, "cat-file", "-s", object)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", ContractPath, commit, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: its size %q: %w", ContractPath, commit, out, err)
	}
	if size > MaxContractBytes {
		return nil, fmt.Errorf("%s at %s is %d bytes, over %d", ContractPath, commit, size, MaxContractBytes)
	}
	raw, err := gitexec.HubOutput(ctx, repo, nil, "cat-file", "blob", object)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", ContractPath, commit, err)
	}
	return raw, nil
}
