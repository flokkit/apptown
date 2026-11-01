package execution

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

// refusedWith is what AppTown makes of a worker answer.
func refusedWith(write func(http.ResponseWriter)) error {
	rec := httptest.NewRecorder()
	write(rec)
	return WorkerRefusal(rec.Result())
}

// rawRefusal answers a body as given, under a content type.
func rawRefusal(status int, contentType, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// classOf names which refusal classes an error is.
func classOf(err error) []RefusalCode {
	var codes []RefusalCode
	for _, c := range refusalClasses {
		if errors.Is(err, c.class) {
			codes = append(codes, c.code)
		}
	}
	return codes
}

// Every class the worker writes is the class AppTown reads, and only it,
// however long its words: the worker cuts them to what AppTown reads whole.
func TestEveryRefusalClassCrossesTheWire(t *testing.T) {
	// Each byte of these escapes to six in JSON.
	long := strings.Repeat("<\x01", maxRefusalBody)
	for _, c := range refusalClasses {
		for name, why := range map[string]string{"short": "why", "long": long} {
			err := refusedWith(func(w http.ResponseWriter) {
				WriteRefusal(w, http.StatusUnprocessableEntity, fmt.Errorf("%w: %s", c.class, why))
			})
			if got := classOf(err); len(got) != 1 || got[0] != c.code {
				t.Errorf("%s, %s: read as %v (%.200v)", c.code, name, got, err)
			}
		}
	}
}

// A start refused because its admission expired is expired: the first class
// an error wraps names it.
func TestARefusalOfTwoClassesIsTheFirst(t *testing.T) {
	err := refusedWith(func(w http.ResponseWriter) {
		WriteRefusal(w, http.StatusUnprocessableEntity, fmt.Errorf("%w: %w", ErrStartRefused, ErrExpired))
	})
	if got := classOf(err); len(got) != 1 || got[0] != refusalExpired {
		t.Fatalf("read as %v (%v)", got, err)
	}
}

// The code decides the class, never the words: other words keep it, and a
// class's words without its code are no class.
func TestARefusalsWordsDoNotDecideItsClass(t *testing.T) {
	reworded := refusedWith(rawRefusal(http.StatusUnprocessableEntity, RefusalContentType,
		`{"code":"start-refused","detail":"the daemon would not come up"}`))
	if !errors.Is(reworded, ErrStartRefused) {
		t.Errorf("reworded: %v", reworded)
	}
	for name, write := range map[string]func(http.ResponseWriter){
		"plain text": rawRefusal(http.StatusUnprocessableEntity, "text/plain; charset=utf-8", ErrStartRefused.Error()+": broken"),
		"no code":    rawRefusal(http.StatusUnprocessableEntity, RefusalContentType, `{"detail":"`+ErrUnsupported.Error()+`"}`),
	} {
		if got := classOf(refusedWith(write)); len(got) != 0 {
			t.Errorf("%s: read as %v", name, got)
		}
	}
}

// What AppTown does not recognise is never a class, whatever its status:
// an unknown code, no code, a broken body or plain text, on a 422 or a 409.
// A 5xx, or a body that cannot be read whole, is the worker unavailable,
// even when what was read is a known code.
func TestAnUnrecognisedRefusalIsNoClass(t *testing.T) {
	coded := func(w http.ResponseWriter) {
		WriteRefusal(w, http.StatusUnprocessableEntity, fmt.Errorf("%w: x", ErrUnsupported))
	}
	type answer struct {
		write       func(http.ResponseWriter)
		cut         bool // the body ends in a read error after what was written
		unavailable bool
	}
	cases := map[string]answer{
		"a 5xx": {write: func(w http.ResponseWriter) {
			WriteRefusal(w, http.StatusInternalServerError, fmt.Errorf("%w: x", ErrUnsupported))
		}, unavailable: true},
		"a known code in a body cut short": {write: coded, cut: true, unavailable: true},
	}
	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusConflict} {
		for name, body := range map[string]struct{ contentType, body string }{
			"unknown code": {RefusalContentType, `{"code":"later-class","detail":"x"}`},
			"no code":      {RefusalContentType, `{"detail":"x"}`},
			"broken body":  {RefusalContentType, `{"code":"conflict"`},
			"plain text":   {"text/plain; charset=utf-8", ErrTaskConflict.Error()},
			// Whole JSON within what is read, more beyond it: not read whole.
			"a known code in a body over the limit": {RefusalContentType, `{"code":"conflict","detail":"x"}` + strings.Repeat(" ", maxRefusalBody)},
		} {
			cases[fmt.Sprintf("%s on %d", name, status)] = answer{write: rawRefusal(status, body.contentType, body.body)}
		}
	}
	for name, c := range cases {
		rec := httptest.NewRecorder()
		c.write(rec)
		resp := rec.Result()
		if c.cut {
			resp.Body = io.NopCloser(io.MultiReader(resp.Body, iotest.ErrReader(io.ErrUnexpectedEOF)))
		}
		err := WorkerRefusal(resp)
		if got := classOf(err); err == nil || len(got) != 0 || errors.Is(err, ErrWorkerUnavailable) != c.unavailable {
			t.Errorf("%s: read as %v (%v)", name, got, err)
		}
	}
}
