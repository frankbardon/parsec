package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/frankbardon/parsec/internal/metrics"
)

// TestMiddleware_RecordsTwirpMethod verifies the Twirp path is reduced
// to the method-only label and the counter/histogram both increment.
func TestMiddleware_RecordsTwirpMethod(t *testing.T) {
	m := metrics.New()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(metrics.Middleware(m, next))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/twirp/parsec.ParsecService/Publish", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !called {
		t.Fatal("inner handler not called")
	}

	if got := testutil.ToFloat64(m.RPCRequestsTotal.WithLabelValues("Publish", "200")); got != 1 {
		t.Errorf("rpc_requests_total = %v, want 1", got)
	}
	count := testutil.CollectAndCount(m.RPCDuration, "parsec_rpc_duration_seconds")
	if count == 0 {
		t.Errorf("rpc_duration_seconds: no series observed")
	}
}

// TestMiddleware_NonRPCPathLabel ensures arbitrary paths collapse to
// the bounded sentinel so attackers can't blow up cardinality by
// hitting random URLs.
func TestMiddleware_NonRPCPathLabel(t *testing.T) {
	m := metrics.New()
	srv := httptest.NewServer(metrics.Middleware(m, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	for _, p := range []string{"/healthz", "/metrics", "/random/path"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	got := testutil.ToFloat64(m.RPCRequestsTotal.WithLabelValues("non_rpc", "200"))
	if got != 3 {
		t.Errorf("non_rpc count = %v, want 3", got)
	}
}

// TestMiddleware_CapturesNon200Status verifies the status label
// reflects the actual response code.
func TestMiddleware_CapturesNon200Status(t *testing.T) {
	m := metrics.New()
	srv := httptest.NewServer(metrics.Middleware(m, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/twirp/parsec.ParsecService/Publish", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := testutil.ToFloat64(m.RPCRequestsTotal.WithLabelValues("Publish", "500")); got != 1 {
		t.Errorf("Publish/500 = %v, want 1", got)
	}
}

// TestMiddleware_NilMetricsBypasses verifies nil Metrics returns the
// next handler unchanged — the wiring is safe even when no observer
// is configured.
func TestMiddleware_NilMetricsBypasses(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { called = true })
	h := metrics.Middleware(nil, next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("nil Metrics should pass through")
	}
}
