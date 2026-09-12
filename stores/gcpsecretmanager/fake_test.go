package gcpsecretmanager

import (
	"context"
	"fmt"
	"hash/crc32"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeSecretManager is enough of the Secret Manager API to exercise the
// store's concurrency contract: etag-checked UpdateSecret, append-only
// versions, and a "latest" alias that resolves to the newest version
// whatever its state. Those are the three behaviors the compare-and-set
// in Save is built on, so a fake that got them wrong would prove nothing.
type fakeSecretManager struct {
	secretmanagerpb.UnimplementedSecretManagerServiceServer

	mu      sync.Mutex
	secrets map[string]*fakeSecret

	// faults injects an error for the named RPC. It runs under the lock,
	// before the call is served, and is how a test makes a poll fail — or
	// mutates state mid-call to stage a race — without tearing the server
	// down. It must not re-enter the fake's own locking helpers.
	faults func(method string) error

	// etagMismatch is what UpdateSecret returns when the supplied etag is
	// stale. It defaults to the documented code; tests flip it because
	// Google's own docs have described both over time, and a store that
	// only recognized one would treat a lost race as a hard failure.
	//
	// https://cloud.google.com/secret-manager/docs/etags: "If an ETag is
	// provided and matches the current resource ETag, the request
	// succeeds; otherwise, it fails with a FAILED_PRECONDITION error and
	// an HTTP status code 400."
	etagMismatch codes.Code

	calls map[string]int
}

type fakeSecret struct {
	pb       *secretmanagerpb.Secret
	etag     int
	versions []*fakeVersion
}

type fakeVersion struct {
	data  []byte
	crc   int64
	state secretmanagerpb.SecretVersion_State
}

func newFakeSecretManager() *fakeSecretManager {
	return &fakeSecretManager{
		secrets:      map[string]*fakeSecret{},
		calls:        map[string]int{},
		etagMismatch: codes.FailedPrecondition,
	}
}

// enter serializes one RPC and applies any injected fault.
func (f *fakeSecretManager) enter(method string) (func(), error) {
	f.mu.Lock()
	f.calls[method]++
	if f.faults != nil {
		if err := f.faults(method); err != nil {
			f.mu.Unlock()
			return nil, err
		}
	}
	return f.mu.Unlock, nil
}

// setEtagMismatchCode chooses what a stale etag looks like on the wire.
func (f *fakeSecretManager) setEtagMismatchCode(c codes.Code) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.etagMismatch = c
}

// bumpEtag moves the secret's etag as a concurrent writer would. It
// assumes the caller already holds the lock — it is called from inside a
// faults hook.
func (f *fakeSecretManager) bumpEtagLocked(name string) {
	if sec, ok := f.secrets[name]; ok {
		sec.etag++
	}
}

func (f *fakeSecretManager) setFaults(fn func(method string) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = fn
}

func (f *fakeSecretManager) callCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeSecretManager) CreateSecret(_ context.Context, req *secretmanagerpb.CreateSecretRequest) (*secretmanagerpb.Secret, error) {
	done, err := f.enter("CreateSecret")
	if err != nil {
		return nil, err
	}
	defer done()

	name := req.GetParent() + "/secrets/" + req.GetSecretId()
	if _, ok := f.secrets[name]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "secret %s already exists", name)
	}
	sec := &fakeSecret{pb: &secretmanagerpb.Secret{
		Name:        name,
		Labels:      req.GetSecret().GetLabels(),
		Replication: req.GetSecret().GetReplication(),
	}}
	f.secrets[name] = sec
	return f.snapshot(sec), nil
}

func (f *fakeSecretManager) GetSecret(_ context.Context, req *secretmanagerpb.GetSecretRequest) (*secretmanagerpb.Secret, error) {
	done, err := f.enter("GetSecret")
	if err != nil {
		return nil, err
	}
	defer done()

	sec, ok := f.secrets[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %s not found", req.GetName())
	}
	return f.snapshot(sec), nil
}

func (f *fakeSecretManager) UpdateSecret(_ context.Context, req *secretmanagerpb.UpdateSecretRequest) (*secretmanagerpb.Secret, error) {
	done, err := f.enter("UpdateSecret")
	if err != nil {
		return nil, err
	}
	defer done()

	name := req.GetSecret().GetName()
	sec, ok := f.secrets[name]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %s not found", name)
	}
	if tag := req.GetSecret().GetEtag(); tag != "" && tag != etagOf(sec.etag) {
		return nil, status.Errorf(f.etagMismatch, "etag mismatch: have %s, got %s", etagOf(sec.etag), tag)
	}
	for _, path := range req.GetUpdateMask().GetPaths() {
		if path != "annotations" {
			return nil, status.Errorf(codes.InvalidArgument, "unsupported update path %q", path)
		}
		sec.pb.Annotations = req.GetSecret().GetAnnotations()
	}
	sec.etag++
	return f.snapshot(sec), nil
}

func (f *fakeSecretManager) AddSecretVersion(_ context.Context, req *secretmanagerpb.AddSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	done, err := f.enter("AddSecretVersion")
	if err != nil {
		return nil, err
	}
	defer done()

	sec, ok := f.secrets[req.GetParent()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %s not found", req.GetParent())
	}
	data := req.GetPayload().GetData()
	if want := req.GetPayload().GetDataCrc32C(); want != 0 {
		if got := int64(crc32.Checksum(data, crc32c)); got != want {
			return nil, status.Error(codes.InvalidArgument, "payload checksum mismatch")
		}
	}
	sec.versions = append(sec.versions, &fakeVersion{
		data:  data,
		crc:   req.GetPayload().GetDataCrc32C(),
		state: secretmanagerpb.SecretVersion_ENABLED,
	})
	return &secretmanagerpb.SecretVersion{
		Name:  fmt.Sprintf("%s/versions/%d", sec.pb.GetName(), len(sec.versions)),
		State: secretmanagerpb.SecretVersion_ENABLED,
	}, nil
}

func (f *fakeSecretManager) AccessSecretVersion(_ context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	done, err := f.enter("AccessSecretVersion")
	if err != nil {
		return nil, err
	}
	defer done()

	sec, n, v, err := f.resolve(req.GetName())
	if err != nil {
		return nil, err
	}
	if v.state != secretmanagerpb.SecretVersion_ENABLED {
		return nil, status.Errorf(codes.FailedPrecondition, "version %d is %s", n, v.state)
	}
	crc := v.crc
	return &secretmanagerpb.AccessSecretVersionResponse{
		Name:    fmt.Sprintf("%s/versions/%d", sec.pb.GetName(), n),
		Payload: &secretmanagerpb.SecretPayload{Data: v.data, DataCrc32C: &crc},
	}, nil
}

func (f *fakeSecretManager) GetSecretVersion(_ context.Context, req *secretmanagerpb.GetSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	done, err := f.enter("GetSecretVersion")
	if err != nil {
		return nil, err
	}
	defer done()

	sec, n, v, err := f.resolve(req.GetName())
	if err != nil {
		return nil, err
	}
	return &secretmanagerpb.SecretVersion{
		Name:  fmt.Sprintf("%s/versions/%d", sec.pb.GetName(), n),
		State: v.state,
	}, nil
}

func (f *fakeSecretManager) ListSecretVersions(_ context.Context, req *secretmanagerpb.ListSecretVersionsRequest) (*secretmanagerpb.ListSecretVersionsResponse, error) {
	done, err := f.enter("ListSecretVersions")
	if err != nil {
		return nil, err
	}
	defer done()

	sec, ok := f.secrets[req.GetParent()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "secret %s not found", req.GetParent())
	}
	out := make([]*secretmanagerpb.SecretVersion, 0, len(sec.versions))
	for i, v := range sec.versions {
		out = append(out, &secretmanagerpb.SecretVersion{
			Name:  fmt.Sprintf("%s/versions/%d", sec.pb.GetName(), i+1),
			State: v.state,
		})
	}
	return &secretmanagerpb.ListSecretVersionsResponse{Versions: out}, nil
}

func (f *fakeSecretManager) DestroySecretVersion(_ context.Context, req *secretmanagerpb.DestroySecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	done, err := f.enter("DestroySecretVersion")
	if err != nil {
		return nil, err
	}
	defer done()

	sec, n, v, err := f.resolve(req.GetName())
	if err != nil {
		return nil, err
	}
	v.state = secretmanagerpb.SecretVersion_DESTROYED
	v.data = nil
	return &secretmanagerpb.SecretVersion{
		Name:  fmt.Sprintf("%s/versions/%d", sec.pb.GetName(), n),
		State: v.state,
	}, nil
}

func (f *fakeSecretManager) DeleteSecret(_ context.Context, req *secretmanagerpb.DeleteSecretRequest) (*emptypb.Empty, error) {
	done, err := f.enter("DeleteSecret")
	if err != nil {
		return nil, err
	}
	defer done()

	delete(f.secrets, req.GetName())
	return &emptypb.Empty{}, nil
}

// resolve maps a version resource name — numeric or the "latest" alias —
// onto its secret and version. Like the real API, "latest" is the highest
// version number regardless of state: it does not skip back to an older
// enabled version.
func (f *fakeSecretManager) resolve(name string) (*fakeSecret, int, *fakeVersion, error) {
	i := strings.LastIndex(name, "/versions/")
	if i < 0 {
		return nil, 0, nil, status.Errorf(codes.InvalidArgument, "%q is not a version name", name)
	}
	sec, ok := f.secrets[name[:i]]
	if !ok {
		return nil, 0, nil, status.Errorf(codes.NotFound, "secret %s not found", name[:i])
	}
	suffix := name[i+len("/versions/"):]
	n := len(sec.versions)
	if suffix != "latest" {
		parsed, err := strconv.Atoi(suffix)
		if err != nil {
			return nil, 0, nil, status.Errorf(codes.InvalidArgument, "bad version %q", suffix)
		}
		n = parsed
	}
	if n < 1 || n > len(sec.versions) {
		return nil, 0, nil, status.Errorf(codes.NotFound, "version %s not found", name)
	}
	return sec, n, sec.versions[n-1], nil
}

func (f *fakeSecretManager) snapshot(sec *fakeSecret) *secretmanagerpb.Secret {
	out := &secretmanagerpb.Secret{
		Name:        sec.pb.GetName(),
		Labels:      sec.pb.GetLabels(),
		Replication: sec.pb.GetReplication(),
		Etag:        etagOf(sec.etag),
	}
	if len(sec.pb.GetAnnotations()) > 0 {
		out.Annotations = map[string]string{}
		for k, v := range sec.pb.GetAnnotations() {
			out.Annotations[k] = v
		}
	}
	return out
}

// versionCount reports how many versions the secret holds, destroyed
// included. Tests use it to prove a rejected write wrote nothing.
func (f *fakeSecretManager) versionCount(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sec, ok := f.secrets[name]
	if !ok {
		return 0
	}
	return len(sec.versions)
}

func (f *fakeSecretManager) versionState(name string, n int) secretmanagerpb.SecretVersion_State {
	f.mu.Lock()
	defer f.mu.Unlock()
	sec, ok := f.secrets[name]
	if !ok || n < 1 || n > len(sec.versions) {
		return secretmanagerpb.SecretVersion_STATE_UNSPECIFIED
	}
	return sec.versions[n-1].state
}

// setAnnotation plants an annotation out of band — how a test simulates
// another node holding the write lease.
func (f *fakeSecretManager) setAnnotation(name, key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sec, ok := f.secrets[name]
	if !ok {
		return
	}
	if sec.pb.Annotations == nil {
		sec.pb.Annotations = map[string]string{}
	}
	sec.pb.Annotations[key] = value
	sec.etag++
}

func etagOf(n int) string { return fmt.Sprintf("%q", "etag-"+strconv.Itoa(n)) }

// startFake runs the fake on a loopback listener and returns a client
// pointed at it. Everything is torn down with the test.
func startFake(t *testing.T) (*fakeSecretManager, *secretmanager.Client) {
	t.Helper()
	fake := newFakeSecretManager()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	secretmanagerpb.RegisterSecretManagerServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial fake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client, err := secretmanager.NewClient(context.Background(), option.WithGRPCConn(conn))
	if err != nil {
		t.Fatalf("secret manager client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return fake, client
}
