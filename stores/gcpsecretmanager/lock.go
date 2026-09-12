package gcpsecretmanager

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// lockAnnotation is the secret annotation carrying the write lease.
const lockAnnotation = "parsec-keyring-lock"

// Secret Manager has no conditional AddSecretVersion: a version is
// appended unconditionally, and the newest one wins for every reader. Two
// nodes rotating at the same moment would therefore both append, and the
// second would erase the first node's new key while looking like a
// success.
//
// The lease closes that window. The secret resource itself carries an
// etag, and UpdateSecret honors it as a compare-and-set, so writing the
// lease annotation is the one atomic primitive available. A writer takes
// the lease, re-reads the latest version number under it, and only then
// appends — which is what makes the version check in Save meaningful
// instead of advisory.
//
// The lease carries a deadline so a node that dies mid-write does not
// freeze rotation across the fleet forever. Stealing an expired lease is
// itself an etag compare-and-set, so two nodes cannot steal the same one.
// Correctness does not rest on the deadline being generous: the version
// check under the lease still rejects a stale write.
type lease struct {
	owner   string
	expires time.Time
}

func (l lease) encode() string {
	return l.owner + ":" + strconv.FormatInt(l.expires.UnixMilli(), 10)
}

// parseLease reads an annotation value. An unparseable value is treated
// as no lease at all: it cannot be honored, and honoring it forever would
// wedge every rotation behind a typo.
func parseLease(v string) (lease, bool) {
	i := strings.LastIndex(v, ":")
	if i <= 0 {
		return lease{}, false
	}
	ms, err := strconv.ParseInt(v[i+1:], 10, 64)
	if err != nil {
		return lease{}, false
	}
	return lease{owner: v[:i], expires: time.UnixMilli(ms)}, true
}

// acquireLock takes the write lease, waiting out a lease held by another
// node until Config.LockWait elapses or ctx ends. The returned release
// runs the lease down; it is safe to call once, from a defer.
func (s *KeyRingStore) acquireLock(ctx context.Context) (func(), error) {
	giveUp := time.Now().Add(s.lockWait)
	backoff := 50 * time.Millisecond
	for {
		sec, err := s.client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: s.secretName})
		if err != nil {
			return nil, fmt.Errorf("gcpsecretmanager: get secret %s: %w", s.secretName, err)
		}
		held, ok := parseLease(sec.GetAnnotations()[lockAnnotation])
		if ok && held.owner != s.nodeID && time.Now().Before(held.expires) {
			if err := s.waitBeforeRetry(ctx, giveUp, &backoff); err != nil {
				return nil, fmt.Errorf("gcpsecretmanager: keyring write lease held by %q for another %s: %w",
					held.owner, time.Until(held.expires).Truncate(time.Millisecond), err)
			}
			continue
		}

		mine := lease{owner: s.nodeID, expires: time.Now().Add(s.lockTTL)}
		if _, err := s.setLockAnnotation(ctx, sec, mine.encode()); err != nil {
			if !isContention(err) {
				return nil, fmt.Errorf("gcpsecretmanager: take keyring write lease: %w", err)
			}
			// Another node updated the secret between our read and our
			// write. Re-read and decide again — it may have taken the
			// lease, or merely released one.
			if werr := s.waitBeforeRetry(ctx, giveUp, &backoff); werr != nil {
				return nil, fmt.Errorf("gcpsecretmanager: take keyring write lease: %w", werr)
			}
			continue
		}
		return func() { s.releaseLock(ctx) }, nil
	}
}

// waitBeforeRetry sleeps out one backoff step, or reports why it will not
// keep waiting.
func (s *KeyRingStore) waitBeforeRetry(ctx context.Context, giveUp time.Time, backoff *time.Duration) error {
	if time.Now().After(giveUp) {
		return fmt.Errorf("gave up after %s", s.lockWait)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(*backoff):
	}
	if *backoff < time.Second {
		*backoff *= 2
	}
	return nil
}

// releaseLock drops this node's lease. Best effort by design: the lease
// expires on its own, so a failure here costs at most one LockTTL of
// rotation latency, and never correctness.
//
// It runs on a context detached from the caller's, because the common
// reason to be releasing is that the caller's deadline just expired.
func (s *KeyRingStore) releaseLock(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	sec, err := s.client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: s.secretName})
	if err != nil {
		s.reportError(fmt.Errorf("gcpsecretmanager: release keyring write lease: %w", err))
		return
	}
	held, ok := parseLease(sec.GetAnnotations()[lockAnnotation])
	if !ok || held.owner != s.nodeID {
		// Expired and stolen, or already released. Not ours to clear.
		return
	}
	if _, err := s.setLockAnnotation(ctx, sec, ""); err != nil && !isContention(err) {
		s.reportError(fmt.Errorf("gcpsecretmanager: release keyring write lease: %w", err))
	}
}

// setLockAnnotation writes the lease annotation with sec's etag as the
// precondition. An empty value removes it. The other annotations are
// carried over: the update mask replaces the whole map.
func (s *KeyRingStore) setLockAnnotation(ctx context.Context, sec *secretmanagerpb.Secret, value string) (*secretmanagerpb.Secret, error) {
	ann := maps.Clone(sec.GetAnnotations())
	if ann == nil {
		ann = map[string]string{}
	}
	if value == "" {
		delete(ann, lockAnnotation)
	} else {
		ann[lockAnnotation] = value
	}
	return s.client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{
		Secret: &secretmanagerpb.Secret{
			Name:        s.secretName,
			Etag:        sec.GetEtag(),
			Annotations: ann,
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"annotations"}},
	})
}

// isContention reports whether err is Secret Manager refusing a write
// because the etag moved. The API is documented to use ABORTED and has
// been observed to use FAILED_PRECONDITION; both mean "re-read and try
// again", and treating either as fatal would turn a retryable race into a
// failed rotation.
func isContention(err error) bool {
	switch status.Code(err) {
	case codes.Aborted, codes.FailedPrecondition:
		return true
	default:
		return false
	}
}
