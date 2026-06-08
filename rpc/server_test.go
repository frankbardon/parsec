package rpc_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/twitchtv/twirp"

	"github.com/frankbardon/parsec/rpc"
)

// fakeService satisfies the generated ParsecService interface for handler
// tests. Each method returns a sentinel response or an injected twirp
// error.
type fakeService struct {
	manifestCalled int
	listCalled     int
	openErr        error
	refreshErr     error
}

func (f *fakeService) Manifest(_ context.Context, _ *rpc.Empty) (*rpc.JSONResponse, error) {
	f.manifestCalled++
	return &rpc.JSONResponse{Payload: []byte(`{"ok":true}`)}, nil
}
func (f *fakeService) OpenPublic(_ context.Context, _ *rpc.OpenPublicRequest) (*rpc.ChannelResponse, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return &rpc.ChannelResponse{Channel: &rpc.ChannelSummary{Name: "public:test.x.y", State: "open"}}, nil
}
func (f *fakeService) CreatePrivate(_ context.Context, _ *rpc.CreatePrivateRequest) (*rpc.Credentials, error) {
	return &rpc.Credentials{Name: "private:test.x.1", AccessToken: "a", RefreshToken: "r"}, nil
}
func (f *fakeService) RefreshToken(_ context.Context, in *rpc.RefreshTokenRequest) (*rpc.RefreshTokenResponse, error) {
	if f.refreshErr != nil {
		return nil, f.refreshErr
	}
	if in.GetRefreshToken() == "" {
		return nil, twirp.NewError(twirp.PermissionDenied, "empty refresh")
	}
	return &rpc.RefreshTokenResponse{AccessToken: "new", AccessExpiresUnix: 12345}, nil
}
func (f *fakeService) ListChannels(_ context.Context, _ *rpc.Empty) (*rpc.ListChannelsResponse, error) {
	f.listCalled++
	return &rpc.ListChannelsResponse{}, nil
}
func (f *fakeService) GetChannel(_ context.Context, _ *rpc.ChannelRef) (*rpc.ChannelResponse, error) {
	return &rpc.ChannelResponse{}, nil
}
func (f *fakeService) DeleteChannel(_ context.Context, _ *rpc.ChannelRef) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) Publish(_ context.Context, _ *rpc.PublishRequest) (*rpc.PublishResponse, error) {
	return &rpc.PublishResponse{Offset: 1, Epoch: "e"}, nil
}
func (f *fakeService) Presence(_ context.Context, _ *rpc.ChannelRef) (*rpc.PresenceResponse, error) {
	return &rpc.PresenceResponse{Subscribers: 0}, nil
}
func (f *fakeService) ListKeys(_ context.Context, _ *rpc.Empty) (*rpc.ListKeysResponse, error) {
	return &rpc.ListKeysResponse{ActiveKeyId: "k-test"}, nil
}
func (f *fakeService) GenerateKey(_ context.Context, _ *rpc.GenerateKeyRequest) (*rpc.KeySummary, error) {
	return &rpc.KeySummary{Id: "k-new", Role: "verify-only"}, nil
}
func (f *fakeService) PromoteKey(_ context.Context, _ *rpc.KeyRef) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) RetireKey(_ context.Context, _ *rpc.KeyRef) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) ReloadKeys(_ context.Context, _ *rpc.Empty) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) IssueMgmt(_ context.Context, _ *rpc.IssueMgmtRequest) (*rpc.IssueMgmtResponse, error) {
	return &rpc.IssueMgmtResponse{MgmtToken: "new", ExpiresUnix: 12345, SignedByKeyId: "k-test"}, nil
}
func (f *fakeService) DlqList(_ context.Context, _ *rpc.DlqListRequest) (*rpc.DlqListResponse, error) {
	return &rpc.DlqListResponse{}, nil
}
func (f *fakeService) DlqCount(_ context.Context, _ *rpc.DlqCountRequest) (*rpc.DlqCountResponse, error) {
	return &rpc.DlqCountResponse{Count: 0}, nil
}
func (f *fakeService) DlqDiscard(_ context.Context, _ *rpc.DlqDiscardRequest) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) DlqReplay(_ context.Context, _ *rpc.DlqReplayRequest) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) RevokeToken(_ context.Context, _ *rpc.RevokeTokenRequest) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}
func (f *fakeService) RevokeUser(_ context.Context, _ *rpc.RevokeUserRequest) (*rpc.Empty, error) {
	return &rpc.Empty{}, nil
}

// newGeneratedServer mounts the generated twirp server with the fake
// service. Bearer enforcement is the responsibility of internal/server's
// middleware — these tests cover only the generated dispatch layer.
func newGeneratedServer(svc rpc.ParsecService) *httptest.Server {
	return httptest.NewServer(rpc.NewParsecServiceServer(svc))
}

func TestGeneratedServer_ManifestPublic(t *testing.T) {
	svc := &fakeService{}
	srv := newGeneratedServer(svc)
	defer srv.Close()
	resp, err := http.Post(srv.URL+rpc.ParsecServicePathPrefix+"Manifest", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
	if svc.manifestCalled != 1 {
		t.Fatalf("manifest call count = %d", svc.manifestCalled)
	}
}

func TestGeneratedServer_ErrorMapping(t *testing.T) {
	cases := []struct {
		code   twirp.ErrorCode
		status int
	}{
		{twirp.InvalidArgument, http.StatusBadRequest},
		{twirp.NotFound, http.StatusNotFound},
		{twirp.AlreadyExists, http.StatusConflict},
		{twirp.FailedPrecondition, http.StatusPreconditionFailed},
		{twirp.PermissionDenied, http.StatusForbidden},
		{twirp.Unauthenticated, http.StatusUnauthorized},
		{twirp.Unavailable, http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(string(c.code), func(t *testing.T) {
			svc := &fakeService{openErr: twirp.NewError(c.code, "test")}
			srv := newGeneratedServer(svc)
			defer srv.Close()
			resp, err := http.Post(srv.URL+rpc.ParsecServicePathPrefix+"OpenPublic", "application/json",
				strings.NewReader(`{"name":"public:test.x.y","ttl_seconds":60}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.status {
				t.Errorf("%s: expected %d, got %d", c.code, c.status, resp.StatusCode)
			}
			var env struct {
				Code string `json:"code"`
				Msg  string `json:"msg"`
			}
			body, _ := io.ReadAll(resp.Body)
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("parse body: %v (%s)", err, body)
			}
			if env.Code != string(c.code) {
				t.Errorf("code = %q, want %q (body=%s)", env.Code, c.code, body)
			}
		})
	}
}

func TestGeneratedServer_RejectsGet(t *testing.T) {
	srv := newGeneratedServer(&fakeService{})
	defer srv.Close()
	resp, err := http.Get(srv.URL + rpc.ParsecServicePathPrefix + "ListChannels")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected non-200, got %d", resp.StatusCode)
	}
}

func TestGeneratedClient_RoundTrip(t *testing.T) {
	svc := &fakeService{}
	srv := newGeneratedServer(svc)
	defer srv.Close()
	c := rpc.NewParsecServiceJSONClient(srv.URL, http.DefaultClient)
	if _, err := c.Manifest(context.Background(), &rpc.Empty{}); err != nil {
		t.Fatal(err)
	}
	res, err := c.RefreshToken(context.Background(), &rpc.RefreshTokenRequest{RefreshToken: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetAccessToken() != "new" {
		t.Fatalf("unexpected access: %q", res.GetAccessToken())
	}
	if svc.manifestCalled != 1 {
		t.Fatalf("manifest call count = %d", svc.manifestCalled)
	}
}
