package apptown

import "net/http"

// This handler is intentionally absent from Handler() and the console API.
func (a *App) InferenceHandler() http.Handler {
	if a.cfg.inferenceURL == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "inference is not configured", http.StatusServiceUnavailable)
		})
	}
	return a.inference
}
