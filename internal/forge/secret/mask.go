package secret

import (
	"context"
	"log"
	"slices"
	"strings"
)

// maskMin is the shortest stored value Mask replaces: shorter values (a
// port, "true") would mask ordinary words.
const maskMin = 8

// Masked is what a masked value reads as.
const Masked = "[secret]"

// Masker reads every value AppTown's stores hold once and returns a
// function that replaces each, eight characters or longer, with
// "[secret]", longest first so a value inside another is not half-masked.
// One snapshot serves a whole operation (a step's evidence, a page of a
// transcript): the stores are not read again for each string. It is a
// convenience against an accidental echo in recorded and shown text, not a
// boundary: a step that transforms a value (base64, a reversed string) is
// not caught.
func (a *Service) Masker(ctx context.Context) func(string) string {
	if a == nil {
		return func(s string) string { return s }
	}
	var values []string
	for _, v := range a.maskValues(ctx) {
		if len(v) >= maskMin {
			values = append(values, v)
		}
	}
	slices.SortFunc(values, func(x, y string) int { return len(y) - len(x) })
	return func(text string) string {
		for _, v := range values {
			text = strings.ReplaceAll(text, v, Masked)
		}
		return text
	}
}

// Mask is Masker for one text.
func (a *Service) Mask(ctx context.Context, text string) string {
	if text == "" {
		return text
	}
	return a.Masker(ctx)(text)
}

// maskValues is every value AppTown's stores hold: the secrets file,
// Infisical's (each, though a file entry of the same name shadows it at
// runtime: an application binding can still resolve the Infisical key), and
// the built-in store's current values. A store that cannot be read gives
// nothing to mask and the log says so: masking is best effort.
func (a *Service) maskValues(ctx context.Context) []string {
	file := a.fileSecrets()
	var out []string
	for _, v := range file {
		out = append(out, v)
	}
	if cfg := infisicalConfig(file); cfg.Configured() {
		remote, err := a.infisicalFetch(context.WithoutCancel(ctx), cfg)
		if err != nil {
			log.Printf("masking: Infisical's values could not be read: %v", err)
		}
		for _, v := range remote {
			out = append(out, v)
		}
	}
	stored, err := a.Vault.Current(ctx)
	if err != nil {
		log.Printf("masking: the built-in store's values could not be read: %v", err)
	}
	return append(out, stored...)
}
