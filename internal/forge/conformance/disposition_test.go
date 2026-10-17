package conformance_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/stamp"
)

func TestDispositionKindsRejectInvalidBoundaryValues(t *testing.T) {
	t.Parallel()
	for _, kind := range []conformance.DispositionKind{conformance.AcceptedRisk, conformance.FalsePositive} {
		body, err := json.Marshal(kind)
		if err != nil || string(body) != `"`+kind.String()+`"` {
			t.Fatalf("JSON: %s %v", body, err)
		}
		var decoded conformance.DispositionKind
		if err := json.Unmarshal(body, &decoded); err != nil || decoded != kind {
			t.Fatalf("decode %v %v", decoded, err)
		}
		value, err := kind.Value()
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []any{value, []byte(kind.String())} {
			if err := decoded.Scan(raw); err != nil || decoded != kind {
				t.Fatalf("SQL decode %v %v", decoded, err)
			}
		}
	}
	for _, raw := range []any{nil, 0, "", "revoked", "running"} {
		kind := conformance.AcceptedRisk
		if err := kind.Scan(raw); !errors.Is(err, conformance.ErrDispositionKind) || kind != conformance.AcceptedRisk {
			t.Fatalf("invalid SQL value %v: %v %v", raw, kind, err)
		}
	}
	for _, raw := range []string{`null`, `""`, `"expired"`, `0`} {
		var kind conformance.DispositionKind
		if err := json.Unmarshal([]byte(raw), &kind); err == nil {
			t.Fatalf("accepted JSON %s", raw)
		}
	}
	for _, kind := range []conformance.DispositionKind{0, 3, 255} {
		if _, err := kind.Value(); !errors.Is(err, conformance.ErrDispositionKind) {
			t.Fatalf("invalid SQL kind %v: %v", kind, err)
		}
		if _, err := json.Marshal(kind); !errors.Is(err, conformance.ErrDispositionKind) {
			t.Fatalf("invalid JSON kind %v: %v", kind, err)
		}
	}
}

func TestExceptionMatchingOwnsSpecificityAndSnapshotMeaning(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	subject := conformance.FindingSubject{Fingerprint: "exact", Identifiers: []string{"CVE-a", "alias"}, Package: "stdlib"}
	ds := []conformance.Disposition{
		{ID: idstest.N(1), Identifier: "alias", Status: conformance.AcceptedRisk},
		{ID: idstest.N(2), Fingerprint: "exact", Status: conformance.FalsePositive, ExpiresAt: stamp.Of(at.Add(-time.Second))},
	}
	if got := conformance.MatchDisposition(subject, ds); got == nil || got.ID != idstest.N(2) {
		t.Fatalf("exact fingerprint lost to earlier broad match: %+v", got)
	}
	// Matching a historical snapshot must not reinterpret it at today's clock.
	if ds[1].ActiveAt(at) {
		t.Fatal("expired disposition is active now")
	}
	for _, expiry := range []stamp.Time{{}, stamp.Of(at.Add(time.Second))} {
		d := ds[1]
		d.ExpiresAt = expiry
		if !d.ActiveAt(at) {
			t.Fatal("live disposition was expired")
		}
	}
	d := ds[1]
	d.ExpiresAt = stamp.Of(at)
	if d.ActiveAt(at) {
		t.Fatal("expiry equality must be inactive")
	}
	if (conformance.Disposition{}).ActiveAt(at) {
		t.Fatal("invalid classification is not active")
	}
	ds[1].Status = 0
	if got := conformance.MatchDisposition(subject, ds); got == nil || got.ID != idstest.N(1) {
		t.Fatalf("invalid exact match suppressed the finding: %+v", got)
	}
	ds[0].Package = "another-package"
	if got := conformance.MatchDisposition(subject, ds); got != nil {
		t.Fatalf("wrong package matched: %+v", got)
	}
	ds[0].Package = "stdlib"
	if got := conformance.MatchDisposition(subject, ds); got == nil || got.ID != idstest.N(1) {
		t.Fatalf("identifier alias/package did not match: %+v", got)
	}
}
