package inference

import (
	"bytes"
	"encoding/json"
)

// Provider-reported counts observed on the trusted transport, not numbers
// submitted by a runtime. Missing usage is explicit, never a zero-cost claim.
type inferenceUsage struct {
	PriceSource  string `json:"priceSource"`
	CacheWrite1h int64  `json:"-"`
	Observed     bool   `json:"observed"`
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheRead    int64  `json:"cacheRead"`
	CacheWrite   int64  `json:"cacheWrite"`
	// Cost is dollars, either provider-reported or calculated from model rates.
	// A provider-reported zero is priced; absent or all-zero model rates are not.
	Cost   float64 `json:"cost,omitempty"`
	Priced bool    `json:"priced,omitempty"`
}

type inferenceUsageReader struct {
	served                           string
	sse                              bool
	pending                          []byte
	overflow                         bool
	usage                            inferenceUsage
	googleCandidates, googleThoughts int64
}

func newInferenceUsageReader() *inferenceUsageReader { return &inferenceUsageReader{} }
func (u *inferenceUsageReader) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		end := len(b)
		if i >= 0 {
			end = i + 1
		}
		if len(u.pending)+end > 2<<20 {
			u.overflow = true
			u.pending = nil
		}
		if !u.overflow {
			u.pending = append(u.pending, b[:end]...)
		}
		if u.sse && i >= 0 {
			if !u.overflow {
				line := bytes.TrimSpace(u.pending)
				if bytes.HasPrefix(line, []byte("data:")) {
					u.observe(bytes.TrimSpace(line[5:]))
				}
			}
			u.pending = nil
			u.overflow = false
		}
		b = b[end:]
	}
	return n, nil
}
func (u *inferenceUsageReader) result() inferenceUsage {
	if !u.sse && !u.overflow {
		u.observe(u.pending)
	}
	return u.usage
}
func (u *inferenceUsageReader) observe(b []byte) {
	var event map[string]json.RawMessage
	if json.Unmarshal(b, &event) != nil {
		return
	}
	u.observeModel(event["model"])
	if v, ok := event["modelVersion"]; ok {
		u.observeModel(v)
	}
	usage := event["usage"]
	for _, k := range []string{"message", "response"} {
		if len(event[k]) > 0 {
			var nested map[string]json.RawMessage
			if json.Unmarshal(event[k], &nested) == nil && len(nested["usage"]) > 0 {
				usage = nested["usage"]
				if v, ok := nested["model"]; ok {
					u.observeModel(v)
				}
			}
		}
	}
	google := false
	if len(event["usageMetadata"]) > 0 {
		usage = event["usageMetadata"]
		google = true
	}
	var counts map[string]json.RawMessage
	if json.Unmarshal(usage, &counts) != nil || counts == nil {
		return
	}
	set := func(key string, dst *int64) {
		if v, ok := counts[key]; ok {
			var n int64
			if json.Unmarshal(v, &n) == nil && n >= 0 {
				*dst = n
				u.usage.Observed = true
			}
		}
	}
	if google {
		set("promptTokenCount", &u.usage.Input)
		set("candidatesTokenCount", &u.googleCandidates)
		set("thoughtsTokenCount", &u.googleThoughts)
		u.usage.Output = u.googleCandidates + u.googleThoughts
		set("cachedContentTokenCount", &u.usage.CacheRead)
		return
	}
	// Only a number is a price: null decodes into a float without error,
	// and must stay no price at all.
	if v, ok := counts["cost"]; ok {
		var c *float64
		if json.Unmarshal(v, &c) == nil && c != nil && *c >= 0 {
			u.usage.Cost, u.usage.Priced = *c, true
			u.usage.PriceSource = "provider-reported"
		}
	}
	set("prompt_tokens", &u.usage.Input)
	set("completion_tokens", &u.usage.Output)
	set("input_tokens", &u.usage.Input)
	set("output_tokens", &u.usage.Output)
	set("cache_read_input_tokens", &u.usage.CacheRead)
	set("cache_creation_input_tokens", &u.usage.CacheWrite)
	var creation struct {
		Long int64 `json:"ephemeral_1h_input_tokens"`
	}
	if json.Unmarshal(counts["cache_creation"], &creation) == nil {
		u.usage.CacheWrite1h = creation.Long
	}
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		var details map[string]int64
		if json.Unmarshal(counts[key], &details) == nil {
			if n, ok := details["cached_tokens"]; ok && n >= 0 {
				u.usage.CacheRead = n
			}
			if n, ok := details["cache_write_tokens"]; ok && n >= 0 {
				u.usage.CacheWrite = n
			}
		}
	}
}

// Malformed model fields leave the last valid served identity intact.
func (u *inferenceUsageReader) observeModel(raw json.RawMessage) {
	var model string
	if json.Unmarshal(raw, &model) == nil && model != "" {
		u.served = model
	}
}
