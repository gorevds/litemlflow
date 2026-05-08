package metrics

import "net/http"

// Handler returns an http.HandlerFunc that writes the metrics text body with
// the correct content type. Mount it with:
//
//	r.Get("/metrics", metrics.Handler(reg))
func Handler(reg *Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = reg.WriteText(w)
	}
}
