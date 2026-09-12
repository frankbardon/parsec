// Module github.com/frankbardon/parsec/stores/gcpsecretmanager keeps the
// Google Cloud SDK out of the parsec module's dependency graph: a
// deployment that does not store its keyring in Secret Manager should not
// pay for grpc-go, the GCP auth stack, or their transitive tree.
//
// The module pins a published parsec version rather than replacing it with
// the surrounding checkout, so `go get` resolves it for consumers. For local
// development against an unreleased parsec, use a go workspace at the repo
// root (`go work init . ./stores/gcpsecretmanager`) — consumers ignore it.
module github.com/frankbardon/parsec/stores/gcpsecretmanager

go 1.26.1

require (
	cloud.google.com/go/secretmanager v1.21.0
	github.com/frankbardon/parsec v0.6.0
	google.golang.org/api v0.297.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
)

require (
	cloud.google.com/go/auth v0.23.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/iam v1.12.0 // indirect
	github.com/FZambia/eagle v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/centrifugal/centrifuge v0.38.0 // indirect
	github.com/centrifugal/protocol v0.19.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coreos/go-oidc/v3 v3.19.0 // indirect
	github.com/dolthub/maphash v0.1.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/gammazero/deque v0.2.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.20 // indirect
	github.com/googleapis/gax-go/v2 v2.24.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/maypok86/otter v1.2.4 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/quagmt/udecimal v1.9.0 // indirect
	github.com/redis/go-redis/v9 v9.21.0 // indirect
	github.com/redis/rueidis v1.0.68 // indirect
	github.com/segmentio/asm v1.2.0 // indirect
	github.com/segmentio/encoding v0.5.3 // indirect
	github.com/shadowspore/fossil-delta v0.0.0-20241213113458-1d797d70cbe3 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.67.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.67.0 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.44.0 // indirect
	go.opentelemetry.io/otel/metric v1.44.0 // indirect
	go.opentelemetry.io/otel/sdk v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto v0.0.0-20260715232425-e75dac1f907d // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260715232425-e75dac1f907d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
)
