package secret

import (
	"context"
	"log"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maskMin is the shortest stored value Mask replaces: shorter values (a
// port, "true") would mask ordinary words.
const maskMin = 8

// masked is what a masked value reads as.
const masked = "[secret]"

// Masker reads every value AppTown's stores hold once and returns a
// function that replaces each, eight characters or longer, with
// "[secret]", longest first so a value inside another is not half-masked.
// One snapshot serves a whole operation (a step's evidence, a page of a
// transcript): the stores are not read again for each string. It is a
// convenience against an accidental echo in recorded and shown text, not a
// boundary: a step that transforms a value (base64, a reversed string) is
// not caught.
func (a *Service) Masker(ctx context.Context) func(string) string { return a.Mask(ctx).Text }

// Mask is one read of the values AppTown's stores hold, for masking.
type Mask struct {
	values   []string // eight characters or longer, longest first
	complete bool     // every store was read
	holdBack int      // characters Partial withholds: 0 (the unfinished word) unless a value contains whitespace
}

// Mask reads every value AppTown's stores hold once (see Masker).
func (a *Service) Mask(ctx context.Context) Mask {
	if a == nil {
		return Mask{complete: true}
	}
	all, complete := a.maskValues(ctx)
	var values []string
	for _, v := range all {
		if len(v) >= maskMin {
			values = append(values, v)
		}
	}
	slices.SortFunc(values, func(x, y string) int { return len(y) - len(x) })
	m := Mask{values: values, complete: complete}
	for _, v := range values {
		if strings.IndexFunc(v, unicode.IsSpace) >= 0 {
			m.holdBack = len(values[0]) // the longest value
			break
		}
	}
	return m
}

// Text replaces every whole value in text with "[secret]".
func (m Mask) Text(text string) string {
	for _, v := range m.values {
		text = strings.ReplaceAll(text, v, masked)
	}
	return text
}

// Partial is text that is still being written (a chat's draft) as it may
// be shown: its unfinished last word is withheld, since a value may be
// part-way written there, and every whole value is masked. What is
// withheld never depends on the values: hiding only an ending that starts
// a value would let a reader test guesses by watching which turn into
// "[secret]". When a value contains whitespace a word does not bound it,
// so the last holdBack characters (the longest value's length) are
// withheld instead. Whole values are found in the full text, so one that
// starts before the cut is hidden there too. It answers false when a store
// could not be read: what to mask is then not known, and the text must not
// be shown.
func (m Mask) Partial(text string) (string, bool) {
	if !m.complete {
		return "", false
	}
	cut := strings.LastIndexFunc(text, unicode.IsSpace)
	if cut >= 0 {
		_, size := utf8.DecodeRuneInString(text[cut:])
		cut += size
	} else {
		cut = 0
	}
	if m.holdBack > 0 {
		cut = max(0, len(text)-m.holdBack)
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
	}
	hide := make([]bool, len(text))
	for _, v := range m.values {
		for at := 0; ; {
			i := strings.Index(text[at:], v)
			if i < 0 {
				break
			}
			for j := at + i; j < at+i+len(v); j++ {
				hide[j] = true
			}
			at += i + 1
		}
	}
	var out strings.Builder
	for i := 0; i < cut; i++ {
		if !hide[i] {
			out.WriteByte(text[i])
			continue
		}
		out.WriteString(masked)
		for i+1 < cut && hide[i+1] {
			i++
		}
	}
	return out.String(), true
}

// maskValues is every value AppTown's stores hold: the secrets file,
// Infisical's (each, though a file entry of the same name shadows it at
// runtime: an application binding can still resolve the Infisical key), and
// the built-in store's current values. A store that cannot be read gives
// nothing to mask and the log says so: masking is best effort. An Infisical
// that cannot be read now still has the values it last answered masked.
// complete says every store was read now.
func (a *Service) maskValues(ctx context.Context) (values []string, complete bool) {
	complete = true
	file, err := a.readFileSecrets()
	if err != nil {
		log.Printf("masking: %v", err)
		complete = false
	}
	var out []string
	for _, v := range file {
		out = append(out, v)
	}
	if cfg := infisicalConfig(file); cfg.configured() {
		remote, err := a.infisicalFetch(context.WithoutCancel(ctx), cfg)
		if err != nil {
			log.Printf("masking: Infisical's values could not be read: %v", err)
			remote = a.infisical.lastKnown(cfg)
			complete = false
		}
		for _, v := range remote {
			out = append(out, v)
		}
	}
	stored, err := a.Vault.current(ctx)
	if err != nil {
		log.Printf("masking: the built-in store's values could not be read: %v", err)
		complete = false
	}
	return append(out, stored...), complete
}
