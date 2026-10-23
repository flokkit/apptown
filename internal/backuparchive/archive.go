// Package backuparchive writes and reads a passphrase-protected archive: a
// gzip-compressed tar inside age encryption (https://age-encryption.org),
// with a passphrase recipient (scrypt). Anyone holding the passphrase can open
// one with the age command; nobody without it can read or change it unseen:
// age authenticates every chunk and the end of the stream, so a changed or
// cut-off archive fails as it is read.
//
// It knows nothing of what it carries. A writer adds named entries (bytes, a
// file, a directory tree) in order; a reader returns them in the same order
// and extracts each under an os.Root, so no entry can write outside the
// directory it is extracted into, through "..", an absolute name or a link.
package backuparchive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// MinPassphrase is the shortest passphrase accepted for a new archive.
const MinPassphrase = 12

// workFactor is scrypt's (log2 of its cost) for a new archive; 0 is age's
// default (18, about a second). Tests lower it.
var workFactor int

// ErrPassphrase is an archive the passphrase does not open.
var ErrPassphrase = errors.New("the passphrase does not open this archive")

// Digest is a file's SHA-256 (hex) and size.
type Digest struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Writer adds entries to a new archive. Close finishes it; an archive whose
// Close did not succeed is not one.
type Writer struct {
	enc io.WriteCloser
	gz  *gzip.Writer
	tw  *tar.Writer
	// Left are what Tree passed over: sockets, pipes and devices, which a
	// running program makes and no archive can carry. Paths relative to the
	// tree's root.
	Left []string
}

// NewWriter starts an archive written to dst, encrypted for passphrase.
func NewWriter(dst io.Writer, passphrase string) (*Writer, error) {
	if len(passphrase) < MinPassphrase {
		return nil, fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	if workFactor > 0 {
		r.SetWorkFactor(workFactor)
	}
	enc, err := age.Encrypt(dst, r)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(enc)
	return &Writer{enc: enc, gz: gz, tw: tar.NewWriter(gz)}, nil
}

// Bytes adds a regular file holding data, mode 0600.
func (w *Writer) Bytes(name string, data []byte) error {
	if err := w.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
		return err
	}
	_, err := w.tw.Write(data)
	return err
}

// File adds the regular file at src under name, with its mode, and says
// what was written. A file that changes size while it is read fails.
func (w *Writer) File(name, src string) (Digest, error) {
	f, err := os.Open(src)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Digest{}, err
	}
	if !fi.Mode().IsRegular() {
		return Digest{}, fmt.Errorf("%s is not a regular file", src)
	}
	return w.file(name, f, fi)
}

func (w *Writer) file(name string, f *os.File, fi fs.FileInfo) (Digest, error) {
	if err := w.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
		return Digest{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w.tw, h), io.LimitReader(f, fi.Size()))
	if err != nil {
		return Digest{}, err
	}
	if n != fi.Size() {
		return Digest{}, fmt.Errorf("%s shrank while it was read (%d of %d bytes)", f.Name(), n, fi.Size())
	}
	return Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, nil
}

// Tree adds the directory root under prefix: directories, regular files and
// symbolic links, in lexical order. skip, given each path relative to root
// (slash-separated), leaves that entry out, and a directory with all it
// holds. A file removed while the tree is walked is left out, as are
// sockets, pipes and devices (named in Left); anything else that cannot be
// read fails.
func (w *Writer) Tree(prefix, root string, skip func(rel string) bool) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel != "." && skip != nil && skip(rel) {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && rel != "." {
				return nil
			}
			return err
		}
		name := path.Join(prefix, rel)
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		switch {
		case fi.IsDir():
			return w.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: int64(fi.Mode().Perm()), ModTime: fi.ModTime()})
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return w.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777, ModTime: fi.ModTime()})
		case fi.Mode().IsRegular():
			f, err := os.Open(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			defer f.Close()
			// The size the header states is the open file's.
			if fi, err = f.Stat(); err != nil {
				return err
			}
			_, err = w.file(name, f, fi)
			return err
		default:
			w.Left = append(w.Left, rel)
			return nil
		}
	})
}

// Close finishes the tar, the compression and the encryption, in that order.
func (w *Writer) Close() error {
	if err := w.tw.Close(); err != nil {
		return err
	}
	if err := w.gz.Close(); err != nil {
		return err
	}
	return w.enc.Close()
}

// Reader returns an archive's entries in order.
type Reader struct {
	gz *gzip.Reader
	tr *tar.Reader
}

// NewReader opens an archive read from src with passphrase.
func NewReader(src io.Reader, passphrase string) (*Reader, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	plain, err := age.Decrypt(src, id)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrPassphrase
		}
		return nil, fmt.Errorf("not a readable archive: %w", err)
	}
	gz, err := gzip.NewReader(plain)
	if err != nil {
		return nil, fmt.Errorf("not a readable archive: %w", err)
	}
	return &Reader{gz: gz, tr: tar.NewReader(gz)}, nil
}

// Next is the next entry's header; io.EOF after the last. Its contents are
// read from the Reader, or written by Extract.
func (r *Reader) Next() (*tar.Header, error) { return r.tr.Next() }

func (r *Reader) Read(p []byte) (int, error) { return r.tr.Read(p) }

// Finish reads what remains after the last entry to the end of the
// encrypted stream, so a cut-off or changed end is an error and not taken
// for a whole archive. Call it after Next returned io.EOF.
func (r *Reader) Finish() error {
	if _, err := io.Copy(io.Discard, r.gz); err != nil {
		return fmt.Errorf("the archive's end is damaged: %w", err)
	}
	return r.gz.Close()
}

// Extract writes the current entry, named hdr.Name with prefix stripped,
// under root: a directory, a regular file (with its mode, and its digest
// returned) or a symbolic link. Other kinds are refused, as is a name that
// leaves the root; root refuses, too, any write through a link that leads
// out of it.
func (r *Reader) Extract(root *os.Root, hdr *tar.Header, prefix string) (Digest, error) {
	rel, ok := strings.CutPrefix(hdr.Name, prefix)
	if !ok {
		return Digest{}, fmt.Errorf("entry %q is not under %q", hdr.Name, prefix)
	}
	rel = strings.TrimSuffix(rel, "/")
	if rel == "" {
		rel = "."
	}
	if !fs.ValidPath(rel) {
		return Digest{}, fmt.Errorf("entry %q leaves the directory it is extracted into", hdr.Name)
	}
	mode := fs.FileMode(hdr.Mode).Perm()
	switch hdr.Typeflag {
	case tar.TypeDir:
		if rel == "." {
			return Digest{}, nil
		}
		if err := root.MkdirAll(rel, 0o700); err != nil {
			return Digest{}, err
		}
		// Its owner can always write it: a read-only directory would refuse
		// the entries that follow it.
		return Digest{}, root.Chmod(rel, mode|0o700)
	case tar.TypeSymlink:
		if err := mkParent(root, rel); err != nil {
			return Digest{}, err
		}
		return Digest{}, root.Symlink(hdr.Linkname, rel)
	case tar.TypeReg:
		if err := mkParent(root, rel); err != nil {
			return Digest{}, err
		}
		f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return Digest{}, err
		}
		d, err := copyDigest(f, r.tr, sha256.New())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil && d.Bytes != hdr.Size {
			err = fmt.Errorf("entry %q is cut short", hdr.Name)
		}
		return d, err
	default:
		return Digest{}, fmt.Errorf("entry %q is of a kind this archive never holds", hdr.Name)
	}
}

func mkParent(root *os.Root, rel string) error {
	if dir := path.Dir(rel); dir != "." {
		return root.MkdirAll(dir, 0o700)
	}
	return nil
}

func copyDigest(dst io.Writer, src io.Reader, h hash.Hash) (Digest, error) {
	n, err := io.Copy(io.MultiWriter(dst, h), src)
	return Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, err
}

// Sum is the digest of everything read from src.
func Sum(src io.Reader) (Digest, error) { return copyDigest(io.Discard, src, sha256.New()) }

// SumFile is the digest of the file at p.
func SumFile(p string) (Digest, error) {
	f, err := os.Open(p)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close()
	return Sum(f)
}
