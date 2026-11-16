package workflow

import "testing"

func TestInterpolateInputsRendersEveryJSONType(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, inputs, want string
	}{
		{"string", `{"value":"routine","other":true}`, "routine"},
		{"targets string", `{"value":"[{\"fingerprint\":\"target\"}]"}`, `[{"fingerprint":"target"}]`},
		{"true", `{"value":true}`, "true"},
		{"false", `{"value":false}`, "false"},
		{"number", `{"value":12.50}`, "12.50"},
		{"null", `{"value":null}`, "null"},
		{"object", `{"value": { "nested": true, "text": "two words" }}`, `{"nested":true,"text":"two words"}`},
		{"array", `{"value": [ 1, false, null ]}`, `[1,false,null]`},
		{"absent", `{}`, `(no input "value" passed to this run)`},
		{"not an object", `[]`, "(AppTown could not read this run's inputs)"},
		{"null inputs", `null`, "(AppTown could not read this run's inputs)"},
		{"malformed", `{"value":`, "(AppTown could not read this run's inputs)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Interpolate(t.Context(), nil, "${{inputs.value}}", &Run{InputsJSON: tt.inputs}, nil, "")
			if got != tt.want {
				t.Fatalf("interpolated %q, want %q", got, tt.want)
			}
		})
	}
}
