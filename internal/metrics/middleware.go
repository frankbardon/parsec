package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// rpcPathPrefix is the Twirp service path prefix. Anything beneath this
// is labeled with the trailing method name; everything else collapses to
// the bounded sentinel RPCNonRPC. Hard-coded to avoid a circular import
// against the rpc package (which would pull in protobuf generated code).
const rpcPathPrefix = "/twirp/parsec.ParsecService/"

// Middleware returns an http.Handler that records request count + duration
// against the supplied Metrics bundle.
//
// The method label is the last URL segment for Twirp paths (e.g. "Publish",
// "ListChannels") and the literal "non_rpc" for anything else. This keeps
// cardinality bounded — even a brute-force scan of arbitrary paths can't
// inflate the label set beyond the known method names.
func Middleware(m *Metrics, next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		dur := time.Since(start).Seconds()
		method := methodLabel(r.URL.Path)
		m.RPCRequestsTotal.WithLabelValues(method, strconv.Itoa(rw.status)).Inc()
		m.RPCDuration.WithLabelValues(method).Observe(dur)
	})
}

// methodLabel derives the bounded method label from path. Returns
// RPCNonRPC for any path outside the Twirp service prefix.
func methodLabel(path string) string {
	if !strings.HasPrefix(path, rpcPathPrefix) {
		return RPCNonRPC
	}
	method := strings.TrimPrefix(path, rpcPathPrefix)
	if method == "" || strings.ContainsAny(method, "/?#") {
		return RPCNonRPC
	}
	return method
}

// statusRecorder wraps an http.ResponseWriter to capture the status code.
// We only need it for the metric — the response body and headers pass
// through unchanged.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
		// status remains the default 200
	}
	return s.ResponseWriter.Write(b)
}

// Flush passes through to the underlying writer if it implements Flusher.
// SSE handlers expect this.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
