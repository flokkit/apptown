package containerresult_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/containerresult"
)

// framed is data as a frame sends it: the data, then its trailer.
func framed(data []byte) []byte {
	return append(append([]byte{}, data...), fmt.Sprintf("APPTOWN-STREAM-END %020d %x\n", len(data), sha256.Sum256(data))...)
}

func receive(t *testing.T, stream []byte, chunk int) ([]byte, error) {
	t.Helper()
	var got bytes.Buffer
	r := containerresult.NewReceiver(&got)
	for len(stream) > 0 {
		n := min(chunk, len(stream))
		if _, err := r.Write(stream[:n]); err != nil {
			t.Fatal(err)
		}
		stream = stream[n:]
	}
	return got.Bytes(), r.Verify()
}

func TestAReceiverPassesOnAWholeStreamWithoutItsTrailer(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("copy\n"), 1000)
	for _, chunk := range []int{1, 7, 105, 4096, 1 << 20} {
		got, err := receive(t, framed(data), chunk)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("chunk %d: got %d bytes, %v; want the data whole", chunk, len(got), err)
		}
	}
	if got, err := receive(t, framed(nil), 3); err != nil || len(got) != 0 {
		t.Fatalf("an empty copy: got %q, %v; want it accepted", got, err)
	}
}

func TestAReceiverRefusesAStreamCutShortAnywhere(t *testing.T) {
	t.Parallel()
	whole := framed([]byte("a database dump\nwith its last lines\n"))
	for cut := 0; cut < len(whole); cut++ {
		if _, err := receive(t, whole[:cut], 5); !errors.Is(err, containerresult.ErrCutShort) {
			t.Fatalf("cut at %d of %d: got %v; want it refused", cut, len(whole), err)
		}
	}
}

func TestAReceiverRefusesATrailerThatDoesNotMatch(t *testing.T) {
	t.Parallel()
	data := []byte("the copy")
	lost := append([]byte("the "), framed(data)[8:]...) // "copy" lost before the trailer
	if _, err := receive(t, lost, 3); !errors.Is(err, containerresult.ErrCutShort) {
		t.Fatalf("bytes lost before the trailer: got %v; want it refused", err)
	}
	changed := framed(data)
	changed[0] = 'T'
	if _, err := receive(t, changed, 3); !errors.Is(err, containerresult.ErrCutShort) {
		t.Fatalf("changed bytes: got %v; want it refused", err)
	}
}

func TestATrailerInsideTheDataIsData(t *testing.T) {
	t.Parallel()
	inner := framed([]byte("an earlier copy"))
	data := append(append([]byte{}, inner...), "and more after it"...)
	got, err := receive(t, framed(data), 11)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("got %q, %v; want the data, its own trailer-like line included", got, err)
	}
	if _, err := receive(t, inner[:len(inner)-1], 11); err == nil {
		t.Fatal("a stream ending inside a trailer was accepted")
	}
}
