package conformance

import (
	"encoding/json"
	"testing"
)

func TestWhatIfLabelsKeepComparisonsAndUnknownDistinct(t *testing.T) {
	for _, tc := range []struct {
		outcome string
		want    CheckLabel
	}{{WhatIfNewFailures, CheckFailed}, {WhatIfNoNewFailures, CheckPassed}, {WhatIfCouldNotAssess, CheckUnknown}, {WhatIfNotEvaluated, CheckUnknown}, {WhatIfNotYetJudged, CheckUnknown}} {
		raw, err := json.Marshal(WhatIfProject{Outcome: tc.outcome})
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ StatusLabel CheckLabel }
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusLabel != tc.want {
			t.Fatalf("%s: %s", raw, got.StatusLabel)
		}
	}
	for _, tc := range []struct {
		result string
		want   CheckLabel
	}{{WhatIfWouldFail, CheckFailed}, {WhatIfWouldPass, CheckPassed}, {WhatIfNotAssessed, CheckUnknown}, {WhatIfNotApplicable, CheckNotApplicable}} {
		raw, _ := json.Marshal(WhatIfRow{Result: tc.result})
		var got struct{ StatusLabel CheckLabel }
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusLabel != tc.want {
			t.Fatalf("%s: %s", raw, got.StatusLabel)
		}
	}
}

func TestUnchangedWhatIfRetainsTheKnownVerdict(t *testing.T) {
	for _, tc := range []struct {
		proposed string
		want     CheckLabel
	}{{"conforms", CheckPassed}, {"does-not-conform", CheckFailed}, {"excepted", CheckExcepted}, {"", CheckUnknown}} {
		raw, err := json.Marshal(WhatIfRow{Result: WhatIfUnchanged, Proposed: tc.proposed})
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ StatusLabel CheckLabel }
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.StatusLabel != tc.want {
			t.Fatalf("%s: %s", raw, got.StatusLabel)
		}
	}
}
