package fault

import (
	"errors"
	"fmt"
	"testing"
)

var errSentinel = New(Conflict, "the record changed")

type denial struct{ code string }

func (d *denial) Error() string     { return "no" }
func (d *denial) FaultKind() Kind   { return Forbidden }
func (d *denial) FaultCode() string { return d.code }

func TestKindOfFindsTheFirstClassificationInTheChain(t *testing.T) {
	wrapped := fmt.Errorf("saving: %w", errSentinel)
	if KindOf(wrapped) != Conflict || !errors.Is(wrapped, errSentinel) {
		t.Fatalf("a wrapped sentinel keeps its kind and identity: %v", KindOf(wrapped))
	}
	if KindOf(errors.New("plain")) != Internal || KindOf(nil) != Internal {
		t.Fatal("an unclassified error is internal")
	}
	d := fmt.Errorf("deciding: %w", &denial{code: "role"})
	if KindOf(d) != Forbidden || CodeOf(d) != "role" {
		t.Fatalf("an owner's own type classifies itself: %v %q", KindOf(d), CodeOf(d))
	}
	if CodeOf(wrapped) != "" {
		t.Fatal("an uncoded error has no code")
	}
}

func TestWrapKeepsTheCauseAndNilStaysNil(t *testing.T) {
	cause := errors.New("row missing")
	err := Wrap(NotFound, fmt.Errorf("change %d: %w", 7, cause))
	if KindOf(err) != NotFound || !errors.Is(err, cause) || err.Error() != "change 7: row missing" {
		t.Fatalf("wrap: %v", err)
	}
	if Wrap(NotFound, nil) != nil {
		t.Fatal("wrapping nil must stay nil")
	}
	if Wrap(Invalid, cause).Error() != "row missing" {
		t.Fatal("wrap without a message reads as the cause")
	}
}

func TestMessageKeepsASentinelsDetailAndAnOwnTypesWords(t *testing.T) {
	err := fmt.Errorf("%w: draft 4 cannot be superseded", errSentinel)
	if Message(err) != "the record changed: draft 4 cannot be superseded" {
		t.Fatalf("a sentinel's detail must read: %q", Message(err))
	}
	if got := Message(fmt.Errorf("handler: %w", &denial{code: "role"})); got != "no" {
		t.Fatalf("an owner's own type reads as itself: %q", got)
	}
	if Message(errors.New("plain")) != "plain" || Message(nil) != "" {
		t.Fatal("an unclassified error reads as itself")
	}
}
