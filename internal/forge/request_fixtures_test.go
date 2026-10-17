package forge

import (
	"net/http"
	"net/http/httptest"
	"strings"
)

// tgReq drives a handler directly, wiring the path values and actor the mux
// would otherwise set, and returns the recorder.
func tgReq(h func(http.ResponseWriter, *http.Request), method, target, body string, pv map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r = testRequestAs(r, "bls")
	for k, v := range pv {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r.WithContext(callerContext(r)))
	return w
}
