package containerresult

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
)

// trailerMark begins a stream's trailer: the mark, the byte count in 20
// digits, a space, the sha256 in 64 hex digits and a newline.
const trailerMark = "APPTOWN-STREAM-END "

// trailerSize is the trailer's fixed length, which the receiver holds back.
const trailerSize = len(trailerMark) + 20 + 1 + 64 + 1

// Framed is command (sh) with a trailer after what it prints on stdout:
// how many bytes it printed and their sha256, counted inside the
// container as they leave. A command that exits non-zero gets no trailer
// and the frame exits with its status. It needs sh, mkfifo, tee, wc,
// sha256sum, tr and cut, which busybox and coreutils both have; a frame
// that cannot be made exits 97, before the command runs.
func Framed(command string) string {
	return `d=/tmp/apptown-stream-$$; mkdir "$d" && mkfifo "$d/h" "$d/n" || exit 97
sha256sum <"$d/h" >"$d/sum" & wc -c <"$d/n" >"$d/size" &
{ ( ` + command + `
); echo $? >"$d/rc"; } | tee "$d/h" "$d/n"
wait; rc=$(cat "$d/rc" 2>/dev/null); [ "$rc" = 0 ] || exit "${rc:-1}"
printf '` + trailerMark + `%020d %s\n' "$(tr -d ' ' <"$d/size")" "$(cut -d' ' -f1 <"$d/sum")"
rm -rf "$d"`
}

// ErrCutShort is a stream whose trailer is missing or does not match what
// arrived: the copy failed.
var ErrCutShort = errors.New("the copy was cut short")

// Receiver passes a framed stream on to its writer without the trailer,
// counting and hashing what it passes, and holds the stream to the
// trailer when it ends (Verify).
type Receiver struct {
	w    io.Writer
	held []byte
	n    int64
	sum  hash.Hash
	err  error
}

// NewReceiver receives a framed stream into w.
func NewReceiver(w io.Writer) *Receiver {
	return &Receiver{w: w, sum: sha256.New()}
}

// Write passes on all but the last TrailerSize bytes received so far.
func (r *Receiver) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	r.held = append(r.held, p...)
	if over := len(r.held) - trailerSize; over > 0 {
		if _, err := r.w.Write(r.held[:over]); err != nil {
			r.err = err
			return 0, err
		}
		r.sum.Write(r.held[:over])
		r.n += int64(over)
		r.held = append(r.held[:0], r.held[over:]...)
	}
	return len(p), nil
}

// Unwrap is the writer the stream is passed on to.
func (r *Receiver) Unwrap() io.Writer { return r.w }

// Verify says whether the stream ended with its trailer and the trailer
// counts exactly what was passed on: ErrCutShort, with what was found,
// when it did not.
func (r *Receiver) Verify() error {
	if r.err != nil {
		return r.err
	}
	t := r.held
	if len(t) != trailerSize || !bytes.HasPrefix(t, []byte(trailerMark)) || t[trailerSize-1] != '\n' || t[len(trailerMark)+20] != ' ' {
		return fmt.Errorf("%w: it ended without its trailer, after %d bytes", ErrCutShort, r.n+int64(len(t)))
	}
	n, err := strconv.ParseInt(string(t[len(trailerMark):len(trailerMark)+20]), 10, 64)
	digest := string(t[len(trailerMark)+21 : trailerSize-1])
	if err != nil || n != r.n {
		return fmt.Errorf("%w: the sender sent %s bytes and %d arrived", ErrCutShort, t[len(trailerMark):len(trailerMark)+20], r.n)
	}
	if got := hex.EncodeToString(r.sum.Sum(nil)); digest != got {
		return fmt.Errorf("%w: the sender's sha256 is %s and what arrived has %s", ErrCutShort, digest, got)
	}
	return nil
}
