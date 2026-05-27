// Package-level WebTransport (HTTP/3) handler. Opt-in alternative to
// WebSocket. The WT transport adapter implements centrifuge.Transport
// directly so the same Node, auth, and channel model power both
// transports.
//
// References:
//   - https://w3c.github.io/webtransport/
//   - https://github.com/quic-go/webtransport-go
//   - centrifuge.Transport interface (transport.go in the centrifuge module)

package server

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/quic-go/quic-go/http3"
	wtgo "github.com/quic-go/webtransport-go"
	"google.golang.org/protobuf/proto"

	"github.com/centrifugal/protocol"
)

// WebTransportOptions configures the optional HTTP/3 WebTransport listener.
type WebTransportOptions struct {
	// Addr is the UDP listen address for the QUIC/HTTP3 listener (e.g. ":8443").
	Addr string
	// TLSCertFile + TLSKeyFile are the cert pair. WebTransport REQUIRES
	// TLS; there is no plaintext mode.
	TLSCertFile string
	TLSKeyFile  string
	// AllowedOrigins gates incoming WT requests. Empty slice means
	// "allow all" (dev mode). In production, list every origin that
	// should be allowed to connect.
	AllowedOrigins []string
}

// Enabled reports whether the WT options describe an active listener.
func (o WebTransportOptions) Enabled() bool { return o.Addr != "" }

// MountWebTransport returns an HTTP handler that upgrades requests to
// WebTransport and bridges each session to a centrifuge.Client. The
// handler is mounted at the path the caller chooses (typically
// "/connection/webtransport"). The returned shutdown function closes
// the WT server on operator request.
func MountWebTransport(node *centrifuge.Node, opts WebTransportOptions, logger *slog.Logger) (http.Handler, error) {
	if !opts.Enabled() {
		return nil, errors.New("webtransport: addr is required to enable WT")
	}
	if opts.TLSCertFile == "" || opts.TLSKeyFile == "" {
		return nil, errors.New("webtransport: cert + key files are required (WT is TLS-only)")
	}
	cert, err := tls.LoadX509KeyPair(opts.TLSCertFile, opts.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("webtransport: load cert: %w", err)
	}
	allowed := slices.Clone(opts.AllowedOrigins)
	wtServer := &wtgo.Server{
		H3: &http3.Server{
			Addr:      opts.Addr,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h3"}},
		},
		CheckOrigin: func(r *http.Request) bool {
			if len(allowed) == 0 {
				return true
			}
			origin := r.Header.Get("Origin")
			return slices.Contains(allowed, origin)
		},
	}
	go func() {
		if err := wtServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("webtransport server exited", "err", err)
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := wtServer.Upgrade(w, r)
		if err != nil {
			http.Error(w, "webtransport upgrade failed", http.StatusBadRequest)
			return
		}
		go handleSession(context.Background(), node, session, logger)
	}), nil
}

// handleSession serves one WebTransport session: it accepts a single
// bidirectional stream as the client connection, instantiates a
// centrifuge.Client, and pumps incoming protocol commands.
func handleSession(ctx context.Context, node *centrifuge.Node, session *wtgo.Session, logger *slog.Logger) {
	stream, err := session.AcceptStream(ctx)
	if err != nil {
		_ = session.CloseWithError(0, "no stream")
		return
	}
	defer stream.Close()
	tp := newWTTransport(session, stream)
	client, closeFn, err := centrifuge.NewClient(ctx, node, tp)
	if err != nil {
		logger.Warn("webtransport: NewClient", "err", err)
		_ = session.CloseWithError(1, "client init failed")
		return
	}
	defer func() { _ = closeFn() }()

	// Read loop: length-prefixed protobuf commands. WebTransport gives
	// us a reliable bidi stream; we frame commands ourselves.
	for {
		var lenBuf [4]byte
		if _, err := readFull(stream, lenBuf[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n == 0 || n > 1<<20 { // 1 MiB cap
			return
		}
		buf := make([]byte, n)
		if _, err := readFull(stream, buf); err != nil {
			return
		}
		var cmd protocol.Command
		if err := proto.Unmarshal(buf, &cmd); err != nil {
			return
		}
		client.HandleCommand(&cmd, int(n))
	}
}

// wtTransport is a centrifuge.Transport over a WebTransport bidi stream.
type wtTransport struct {
	mu      sync.Mutex
	session *wtgo.Session
	stream  *wtgo.Stream
	closed  bool
}

func newWTTransport(s *wtgo.Session, stream *wtgo.Stream) *wtTransport {
	return &wtTransport{session: s, stream: stream}
}

func (t *wtTransport) Name() string                          { return "webtransport" }
func (t *wtTransport) Protocol() centrifuge.ProtocolType     { return centrifuge.ProtocolTypeProtobuf }
func (t *wtTransport) ProtocolVersion() centrifuge.ProtocolVersion {
	return centrifuge.ProtocolVersion2
}
func (t *wtTransport) Unidirectional() bool      { return false }
func (t *wtTransport) Emulation() bool           { return false }
func (t *wtTransport) DisabledPushFlags() uint64 { return 0 }
func (t *wtTransport) PingPongConfig() centrifuge.PingPongConfig {
	return centrifuge.PingPongConfig{PingInterval: 25 * time.Second, PongTimeout: 8 * time.Second}
}

func (t *wtTransport) Write(data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("transport closed")
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := t.stream.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := t.stream.Write(data)
	return err
}

func (t *wtTransport) WriteMany(messages ...[]byte) error {
	for _, m := range messages {
		if err := t.Write(m); err != nil {
			return err
		}
	}
	return nil
}

func (t *wtTransport) Close(_ centrifuge.Disconnect) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	_ = t.stream.Close()
	return t.session.CloseWithError(0, "close")
}

// readFull reads exactly len(buf) bytes from r or returns an error.
func readFull(r interface {
	Read(p []byte) (n int, err error)
}, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
