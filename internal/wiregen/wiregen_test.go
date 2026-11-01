package wiregen

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type state uint8

var stateWords = []string{"open", "closed"}

func (s state) String() string {
	if s < 1 || int(s) > len(stateWords) {
		return "invalid"
	}
	return stateWords[s-1]
}
func (s state) Value() (driver.Value, error) {
	if s < 1 || int(s) > len(stateWords) {
		return nil, errors.New("invalid state")
	}
	return s.String(), nil
}
func (s state) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

type Base struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}

type Owner struct {
	Name string `json:"name"`
}

type Record struct {
	Base
	Title    string          `json:"title"`
	Note     string          `json:"note,omitempty"`
	Hidden   string          `json:"-"`
	State    state           `json:"state"`
	Previous *state          `json:"previous"`
	Owner    *Owner          `json:"owner"`
	Tags     []string        `json:"tags"`
	Labels   map[string]int  `json:"labels"`
	Raw      json.RawMessage `json:"raw,omitempty"`
	Inline   struct{ N int } `json:"inline"`
	Untagged bool
	private  int
}

// private exists to show an unexported field is never declared.
var _ = Record{private: 1}

func generator() *typeGenerator {
	g := New()
	g.Map(time.Time{}, "string")
	g.Map(json.RawMessage{}, "unknown")
	return g
}

func TestDeclarationsFollowEncodingJSON(t *testing.T) {
	src, err := generator().Declare(Named{"Row", Record{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export type Row = {",
		"  id: number", "  createdAt: string", "  title: string", "  note?: string",
		"  state: state", "  previous: state | null", "  owner: Owner | null", "  tags: string[]", "  labels: Record<string, number>",
		"  raw?: unknown", "  inline: { N: number }", "  Untagged: boolean",
		`export type state = "open" | "closed"`,
		"export type Owner = {\n  name: string\n}",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	for _, absent := range []string{"Hidden", "private", "Base"} {
		if strings.Contains(src, absent) {
			t.Errorf("%s must not be declared:\n%s", absent, src)
		}
	}
}

// The keys declared for a type are exactly the keys encoding/json writes for a
// fully populated value of it: the generator cannot drift from the encoder.
func TestDeclaredKeysAreTheEncodersKeys(t *testing.T) {
	src, err := generator().Declare(Named{"Row", Record{}})
	if err != nil {
		t.Fatal(err)
	}
	prev := state(2)
	full := Record{Base: Base{ID: 1}, Title: "t", Note: "n", State: 1, Previous: &prev, Owner: &Owner{Name: "o"},
		Tags: []string{"a"}, Labels: map[string]int{"x": 1}, Raw: json.RawMessage(`{}`), Untagged: true}
	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatal(err)
	}
	body := src[strings.Index(src, "export type Row = {"):]
	body = body[:strings.Index(body, "\n}")]
	declared := map[string]bool{}
	for _, line := range strings.Split(body, "\n")[1:] {
		key, _, _ := strings.Cut(strings.TrimSpace(line), ":")
		declared[strings.TrimSuffix(key, "?")] = true
	}
	for k := range keys {
		if !declared[k] {
			t.Errorf("encoder writes %q, generator does not declare it", k)
		}
	}
	for k := range declared {
		if _, ok := keys[k]; !ok {
			t.Errorf("generator declares %q, encoder never writes it", k)
		}
	}
}

type selfEncoding struct{}

func (selfEncoding) MarshalJSON() ([]byte, error) { return []byte(`"x"`), nil }

func TestAnUnmappedSelfEncodingTypeIsAnError(t *testing.T) {
	type holder struct {
		V selfEncoding `json:"v"`
	}
	if _, err := New().Declare(Named{"Holder", holder{}}); err == nil || !strings.Contains(err.Error(), "encodes itself") {
		t.Fatalf("want an error naming the self-encoding type, got %v", err)
	}
}

func TestTwoTypesCannotShareAName(t *testing.T) {
	type a struct{}
	type b struct{}
	_, err := New().Declare(Named{"Same", a{}}, Named{"Same", b{}})
	if err == nil {
		t.Fatal("a name collision must be refused")
	}
}

type extra struct {
	Detail string `json:"detail"`
}

type withOptionalBlock struct {
	Name string `json:"name"`
	*extra
}

// A nil embedded pointer drops its fields from the JSON, so they are optional.
func TestEmbeddedPointerFieldsAreOptional(t *testing.T) {
	src, err := New().Declare(Named{"WithBlock", withOptionalBlock{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "  name: string") || !strings.Contains(src, "  detail?: string") {
		t.Fatalf("got:\n%s", src)
	}
}

func TestANameThatShadowsATypeScriptBuiltinIsRefused(t *testing.T) {
	type Record struct{}
	type holder struct {
		R Record `json:"r"`
	}
	if _, err := New().Declare(Named{"Holder", holder{}}); err == nil || !strings.Contains(err.Error(), "shadows") {
		t.Fatalf("want a refusal naming the shadowing, got %v", err)
	}
}

// A name defined at two depths is the shallower field's, as encoding/json
// decides; two untagged fields at one depth cancel out.
func TestCollidingNamesFollowTheEncodersDominanceRule(t *testing.T) {
	type Inner struct {
		State string `json:"state"`
		Kept  int    `json:"kept"`
	}
	type A struct{ Same int }
	type B struct{ Same int }
	type Outer struct {
		Inner
		A
		B
		State bool `json:"state"`
	}
	src, err := New().Declare(Named{"Outer", Outer{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(src, "state:") != 1 || !strings.Contains(src, "state: boolean") || !strings.Contains(src, "kept: number") || strings.Contains(src, "Same") {
		t.Fatalf("dominance:\n%s", src)
	}
	encoded, _ := json.Marshal(Outer{})
	if strings.Contains(string(encoded), "Same") || strings.Count(string(encoded), `"state"`) != 1 {
		t.Fatalf("the rule mirrors the encoder: %s", encoded)
	}
}
