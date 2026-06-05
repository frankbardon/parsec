package parsec_test

import (
	"errors"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/parsectest"
)

// TestRefreshAccessRotatesPair exercises the happy-path: a JTI-bearing
// refresh trades for a fresh access AND a fresh refresh sharing the
// same family ID, then the predecessor is rejected on second use and
// the family is revoked so siblings die too.
func TestRefreshAccessRotatesPair(t *testing.T) {
	inst := parsectest.New(t)

	creds, err := inst.CreatePrivate("u-1", "private:web.user.1.notifications", 30*time.Minute, nil)
	if err != nil {
		t.Fatalf("CreatePrivate: %v", err)
	}

	originalRefresh := mustClaims(t, inst, creds.RefreshToken, auth.TypeRefresh)
	if originalRefresh.JTI == "" || originalRefresh.FID == "" {
		t.Fatalf("expected JTI + FID on new refresh, got jti=%q fid=%q", originalRefresh.JTI, originalRefresh.FID)
	}

	res, err := inst.RefreshAccess(creds.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshAccess: %v", err)
	}
	if !res.Rotated {
		t.Fatal("expected Rotated=true on JTI-bearing token")
	}
	if res.RefreshToken == "" {
		t.Fatal("expected fresh refresh")
	}
	if res.RefreshToken == creds.RefreshToken {
		t.Fatal("rotated refresh must differ from input")
	}

	rotated := mustClaims(t, inst, res.RefreshToken, auth.TypeRefresh)
	if rotated.JTI == originalRefresh.JTI {
		t.Fatal("rotated JTI must differ")
	}
	if rotated.FID != originalRefresh.FID {
		t.Fatalf("FID drift: %s -> %s", originalRefresh.FID, rotated.FID)
	}

	// Replay the original refresh: must fail, must revoke family.
	if _, err := inst.RefreshAccess(creds.RefreshToken); !isAuthDenied(err) {
		t.Fatalf("replay err=%v, want PARSEC_AUTH_DENIED", err)
	}

	// The previously-issued rotated refresh is now poisoned because
	// reuse detection revoked the family.
	if _, err := inst.RefreshAccess(res.RefreshToken); !isAuthDenied(err) {
		t.Fatalf("sibling refresh err=%v, want PARSEC_AUTH_DENIED", err)
	}
}

// TestRefreshAccessLegacyTokenNoRotation covers the back-compat path:
// a refresh manually minted without JTI / FID still trades for a new
// access but no rotation occurs.
func TestRefreshAccessLegacyTokenNoRotation(t *testing.T) {
	inst := parsectest.New(t)

	if _, err := inst.OpenPublic("public:web.notify.demo", time.Minute); err != nil {
		t.Fatalf("OpenPublic: %v", err)
	}
	// Manually create a private channel without using the issuer so
	// we can sign a refresh that lacks JTI.
	creds, err := inst.CreatePrivate("u-legacy", "private:web.user.legacy.notify", time.Hour, nil)
	if err != nil {
		t.Fatalf("CreatePrivate: %v", err)
	}
	legacyClaims := mustClaims(t, inst, creds.RefreshToken, auth.TypeRefresh)
	legacyClaims.JTI = ""
	legacyClaims.FID = ""
	legacyRefresh, err := mintRefresh(inst, legacyClaims)
	if err != nil {
		t.Fatalf("mint legacy refresh: %v", err)
	}

	res, err := inst.RefreshAccess(legacyRefresh)
	if err != nil {
		t.Fatalf("RefreshAccess legacy: %v", err)
	}
	if res.Rotated {
		t.Fatal("legacy token must not rotate")
	}
	if res.RefreshToken != "" {
		t.Fatal("legacy token must not produce fresh refresh")
	}
	if res.AccessToken == "" {
		t.Fatal("legacy token must still yield access")
	}
}

// TestRefreshAccessExplicitStore wires a custom RefreshStore so the
// test can assert it was consulted. Also confirms the store's records
// outlive the rotation call: a second redemption attempt returns
// PARSEC_AUTH_DENIED without ever reaching the rotated pair issuer.
func TestRefreshAccessExplicitStore(t *testing.T) {
	store := auth.NewMemoryRefreshStore(0)
	t.Cleanup(store.Close)

	inst := parsectest.New(t, parsectest.WithOptions(func(o *parsec.Options) {
		o.RefreshStore = store
	}))

	creds, err := inst.CreatePrivate("u-2", "private:web.user.2.feed", time.Hour, nil)
	if err != nil {
		t.Fatalf("CreatePrivate: %v", err)
	}
	if _, err := inst.RefreshAccess(creds.RefreshToken); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := inst.RefreshAccess(creds.RefreshToken); !isAuthDenied(err) {
		t.Fatalf("second redeem err=%v, want PARSEC_AUTH_DENIED", err)
	}
}

// mustClaims verifies the token and returns its claims.
func mustClaims(t *testing.T, inst *parsectest.Instance, tok string, typ auth.Type) auth.Claims {
	t.Helper()
	claims, err := inst.Verifier().Verify(tok, typ)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return claims
}

// mintRefresh signs a refresh claims directly so tests can construct
// tokens that bypass the rotation-aware issuer (legacy shape, fuzz
// inputs, etc).
func mintRefresh(inst *parsectest.Instance, c auth.Claims) (string, error) {
	signer, err := auth.NewSigner(inst.KeyRing())
	if err != nil {
		return "", err
	}
	c.Typ = auth.TypeRefresh
	return signer.Sign(c)
}

// isAuthDenied reports whether err is a PARSEC_AUTH_DENIED coded
// error.
func isAuthDenied(err error) bool {
	if err == nil {
		return false
	}
	var coded *perr.Error
	if !errors.As(err, &coded) {
		return false
	}
	return coded.Code == perr.AuthDenied
}
