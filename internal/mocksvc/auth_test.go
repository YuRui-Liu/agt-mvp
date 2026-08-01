package mocksvc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func testMockSecret(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return append([]byte(nil), sum[:]...)
}

type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestVerifyReturnsStableIdentity(t *testing.T) {
	now := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	secret := testMockSecret("stable identity")
	first := NewAuthenticator(NewMemoryStore(), secret, func() time.Time { return now })

	if err := first.RequestCode("+86 138-0013-8000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	session1, err := first.Verify("+86 (138) 0013-8000", "246810")
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if err := first.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request second code: %v", err)
	}
	session2, err := first.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("second verify: %v", err)
	}

	second := NewAuthenticator(NewMemoryStore(), secret, func() time.Time { return now.Add(time.Hour) })
	if err := second.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code on second authenticator: %v", err)
	}
	session3, err := second.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("verify on second authenticator: %v", err)
	}

	if session1.Identity.SubjectID != session2.Identity.SubjectID ||
		session1.Identity.SubjectID != session3.Identity.SubjectID {
		t.Fatalf("subject IDs differ: %q, %q, %q", session1.Identity.SubjectID, session2.Identity.SubjectID, session3.Identity.SubjectID)
	}
	if session1.Identity.KuAIID != session2.Identity.KuAIID ||
		session1.Identity.KuAIID != session3.Identity.KuAIID {
		t.Fatalf("kuAI IDs differ: %q, %q, %q", session1.Identity.KuAIID, session2.Identity.KuAIID, session3.Identity.KuAIID)
	}
}

func TestDifferentPhonesReturnDifferentIdentities(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("different phones"), func() time.Time {
		return time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	})

	identities := make([]Identity, 0, 2)
	for _, phone := range []string{"+8613800138000", "+8613800138001"} {
		if err := auth.RequestCode(phone); err != nil {
			t.Fatalf("request code for %q: %v", phone, err)
		}
		session, err := auth.Verify(phone, "246810")
		if err != nil {
			t.Fatalf("verify %q: %v", phone, err)
		}
		identities = append(identities, session.Identity)
	}

	if identities[0].SubjectID == identities[1].SubjectID {
		t.Fatalf("different phones returned subject ID %q", identities[0].SubjectID)
	}
	if identities[0].KuAIID == identities[1].KuAIID {
		t.Fatalf("different phones returned kuAI ID %q", identities[0].KuAIID)
	}
}

func TestNewAuthenticatorRejectsShortMockSecretSafely(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), []byte("short"), time.Now)
	if err := auth.RequestCode("+8613800138000"); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("RequestCode error = %v, want ErrInvalidSecret", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Verify error = %v, want ErrInvalidSecret", err)
	}
	if _, err := auth.Authenticate("token"); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Authenticate error = %v, want ErrInvalidSecret", err)
	}
}

func TestCanonicalPhoneFormsReturnSameIdentity(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("canonical phones"), time.Now)
	phones := []string{
		"138 0013 8000",
		"+86 (138) 0013-8000",
	}
	var first Identity
	for _, phone := range phones {
		if err := auth.RequestCode(phone); err != nil {
			t.Fatalf("RequestCode(%q): %v", phone, err)
		}
		session, err := auth.Verify(phone, "246810")
		if err != nil {
			t.Fatalf("Verify(%q): %v", phone, err)
		}
		if first.SubjectID == "" {
			first = session.Identity
			continue
		}
		if session.Identity.SubjectID != first.SubjectID || session.Identity.KuAIID != first.KuAIID {
			t.Fatalf("phone %q returned %#v, want IDs from %#v", phone, session.Identity, first)
		}
	}
}

func TestBareChineseLookingPhoneAndExplicitInternationalPhoneDiffer(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("explicit international"), time.Now)
	var identities []Identity
	for _, phone := range []string{"14155552671", "+14155552671"} {
		if err := auth.RequestCode(phone); err != nil {
			t.Fatalf("RequestCode(%q): %v", phone, err)
		}
		session, err := auth.Verify(phone, "246810")
		if err != nil {
			t.Fatalf("Verify(%q): %v", phone, err)
		}
		identities = append(identities, session.Identity)
	}
	if identities[0].SubjectID == identities[1].SubjectID {
		t.Fatalf("different phone semantics returned subject ID %q", identities[0].SubjectID)
	}
}

func TestBare165PhoneUsesChineseSemantics(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("bare 165"), time.Now)
	var identities []Identity
	for _, phone := range []string{"16505551234", "+16505551234"} {
		if err := auth.RequestCode(phone); err != nil {
			t.Fatalf("RequestCode(%q): %v", phone, err)
		}
		session, err := auth.Verify(phone, "246810")
		if err != nil {
			t.Fatalf("Verify(%q): %v", phone, err)
		}
		identities = append(identities, session.Identity)
	}
	if identities[0].SubjectID == identities[1].SubjectID {
		t.Fatalf("bare Chinese and explicit international forms returned subject ID %q", identities[0].SubjectID)
	}
}

func TestExplicitDifferentCountriesReturnDifferentIdentities(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("country separation"), time.Now)
	var identities []Identity
	for _, phone := range []string{"+8613800138000", "+14155552671"} {
		if err := auth.RequestCode(phone); err != nil {
			t.Fatalf("RequestCode(%q): %v", phone, err)
		}
		session, err := auth.Verify(phone, "246810")
		if err != nil {
			t.Fatalf("Verify(%q): %v", phone, err)
		}
		identities = append(identities, session.Identity)
	}
	if identities[0].SubjectID == identities[1].SubjectID {
		t.Fatalf("different countries returned subject ID %q", identities[0].SubjectID)
	}
}

func TestChallengeStateDoesNotContainCanonicalPhone(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("challenge digest"), time.Now)
	if err := auth.RequestCode("13800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	state := fmt.Sprint(auth.challenges)
	if strings.Contains(state, "13800138000") || strings.Contains(state, "+8613800138000") {
		t.Fatalf("challenge state contains phone: %s", state)
	}
}

func TestRequestCodeCleansExpiredChallengesBeforeCapacityCheck(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("challenge capacity"), clock.Now)
	for index := range 1024 {
		phone := fmt.Sprintf("+1%010d", index)
		if err := auth.RequestCode(phone); err != nil {
			t.Fatalf("RequestCode(%d): %v", index, err)
		}
	}
	if err := auth.RequestCode("+19999999999"); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("capacity error = %v, want ErrCapacityExceeded", err)
	}
	clock.Advance(5 * time.Minute)
	if err := auth.RequestCode("+18888888888"); err != nil {
		t.Fatalf("request after expiry cleanup: %v", err)
	}
	if got := len(auth.challenges); got != 1 {
		t.Fatalf("challenge count after cleanup = %d, want 1", got)
	}
}

func TestVerifyCleansOtherExpiredChallenges(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("verify challenge cleanup"), clock.Now)
	if err := auth.RequestCode("+14155550001"); err != nil {
		t.Fatalf("request old code: %v", err)
	}
	clock.Advance(4 * time.Minute)
	if err := auth.RequestCode("+14155550002"); err != nil {
		t.Fatalf("request current code: %v", err)
	}
	clock.Advance(time.Minute)
	if _, err := auth.Verify("+14155550002", "246810"); err != nil {
		t.Fatalf("verify current code: %v", err)
	}
	if got := len(auth.challenges); got != 0 {
		t.Fatalf("challenge count after verify cleanup = %d, want 0", got)
	}
}

func TestRequestCodeRejectsInvalidPhones(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("invalid phones"), time.Now)
	for _, phone := range []string{
		"",
		"123456",
		"1234567890123456",
		"+",
		"++8613800138000",
		"+86/13800138000",
		"+86abc13800138000",
		"+01234567890",
		"１２３４５６７８",
		"8613800138000",
		"1234567",
	} {
		if err := auth.RequestCode(phone); !errors.Is(err, ErrInvalidPhone) {
			t.Errorf("RequestCode(%q) error = %v, want ErrInvalidPhone", phone, err)
		}
	}
}

func TestVerifyRequiresRequestedCode(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("code not requested"), time.Now)
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrCodeNotRequested) {
		t.Fatalf("Verify error = %v, want ErrCodeNotRequested", err)
	}
}

func TestOTPErrorScenarioRejectsCorrectMockCode(t *testing.T) {
	auth := NewAuthenticatorForScenario(NewMemoryStore(), testMockSecret("otp error"), time.Now, "otp_error")
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("Verify error=%v want ErrInvalidCode", err)
	}
}

func TestEphemeralChallengeSecretDoesNotChangeIdentity(t *testing.T) {
	store := NewMemoryStore()
	identitySecret := testMockSecret("stable root")
	first := NewAuthenticatorWithChallengeSecret(
		store, identitySecret, testMockSecret("launch one"), fixedTaskClock(), "success",
	)
	second := NewAuthenticatorWithChallengeSecret(
		store, identitySecret, testMockSecret("launch two"), fixedTaskClock(), "success",
	)
	const phone = "+8613800138000"
	if first.challengeKey(phone) == second.challengeKey(phone) {
		t.Fatal("challenge digest did not change across launches")
	}
	var identities []Identity
	for _, auth := range []*Authenticator{first, second} {
		if err := auth.RequestCode(phone); err != nil {
			t.Fatal(err)
		}
		session, err := auth.Verify(phone, "246810")
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, session.Identity)
	}
	if identities[0].SubjectID != identities[1].SubjectID || identities[0].KuAIID != identities[1].KuAIID {
		t.Fatalf("ephemeral challenge secret changed identity: %#v %#v", identities[0], identities[1])
	}
}

func TestVerifyRejectsExpiredCode(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("expired code"), clock.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	clock.Advance(5 * time.Minute)

	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("Verify error = %v, want ErrCodeExpired", err)
	}
}

func TestVerifyLimitsFailedAttempts(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("failed attempts"), time.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}

	for attempt := 1; attempt <= 5; attempt++ {
		if _, err := auth.Verify("+8613800138000", "000000"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d error = %v, want ErrInvalidCode", attempt, err)
		}
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("sixth attempt error = %v, want ErrTooManyAttempts", err)
	}
}

func TestSuccessfulVerifyConsumesChallenge(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("consumed challenge"), time.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrCodeNotRequested) {
		t.Fatalf("reused challenge error = %v, want ErrCodeNotRequested", err)
	}
}

func TestVerifyCreatesFormattedOpaqueIdentity(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("formatted identity"), time.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	session, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !regexp.MustCompile(`^sub-[A-Za-z0-9_-]+$`).MatchString(session.Identity.SubjectID) {
		t.Fatalf("unexpected subject ID %q", session.Identity.SubjectID)
	}
	if !regexp.MustCompile(`^KUAI-[A-Z2-7]{10}$`).MatchString(session.Identity.KuAIID) {
		t.Fatalf("unexpected kuAI ID %q", session.Identity.KuAIID)
	}
}

func TestSubjectIDUsesExplicitSubjectDomain(t *testing.T) {
	secret := testMockSecret("domain separation")
	const normalizedPhone = "+8613800138000"
	mac := hmac.New(sha256.New, secret)
	if _, err := mac.Write([]byte("subject:" + normalizedPhone)); err != nil {
		t.Fatalf("calculate expected HMAC: %v", err)
	}
	want := "sub-" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	auth := NewAuthenticator(NewMemoryStore(), secret, time.Now)
	if err := auth.RequestCode("+86 (138) 0013-8000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	session, err := auth.Verify(normalizedPhone, "246810")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := session.Identity.SubjectID; got != want {
		t.Fatalf("SubjectID = %q, want domain-separated %q", got, want)
	}
}

func TestVerifyPreservesExistingIdentityCreationTime(t *testing.T) {
	store := NewMemoryStore()
	secret := testMockSecret("existing identity")
	createdAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	subjectID := deriveSubjectID(secret, "+8613800138000")
	existing := Identity{
		SubjectID: subjectID,
		KuAIID:    deriveKuAIID(secret, subjectID),
		CreatedAt: createdAt,
	}
	if err := store.Put(existing); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	auth := NewAuthenticator(store, secret, func() time.Time {
		return createdAt.Add(24 * time.Hour)
	})
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	session, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if session.Identity != existing {
		t.Fatalf("identity = %#v, want existing %#v", session.Identity, existing)
	}
}

func TestAuthenticateAcceptsRandomTokenUntilExpiry(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("authenticate token"), clock.Now)
	sessions := make([]AuthSession, 0, 2)
	for range 2 {
		if err := auth.RequestCode("+8613800138000"); err != nil {
			t.Fatalf("request code: %v", err)
		}
		session, err := auth.Verify("+8613800138000", "246810")
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		sessions = append(sessions, session)
	}
	if sessions[0].Token == sessions[1].Token {
		t.Fatal("two sessions returned the same token")
	}
	if len(sessions[0].Token) < 40 {
		t.Fatalf("token is unexpectedly short: %d", len(sessions[0].Token))
	}
	identity, err := auth.Authenticate(sessions[0].Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if identity != sessions[0].Identity {
		t.Fatalf("identity = %#v, want %#v", identity, sessions[0].Identity)
	}
	if _, err := auth.Authenticate("unknown-token"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown token error = %v, want ErrUnauthenticated", err)
	}

	clock.Advance(15 * time.Minute)
	if _, err := auth.Authenticate(sessions[0].Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token error = %v, want ErrUnauthenticated", err)
	}
	if err := auth.RequestCode(sessions[0].Token); !errors.Is(err, ErrInvalidPhone) || stringsContain(err.Error(), sessions[0].Token) {
		t.Fatalf("unsafe token error: %v", err)
	}
}

func TestSessionStateStoresOnlyTokenDigest(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("session digest"), time.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request code: %v", err)
	}
	session, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if state := fmt.Sprint(auth.sessions); strings.Contains(state, session.Token) {
		t.Fatalf("session state contains raw token: %s", state)
	}
}

func TestAuthenticateCleansExpiredSessions(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("session cleanup"), clock.Now)
	for range 10 {
		if err := auth.RequestCode("+8613800138000"); err != nil {
			t.Fatalf("request code: %v", err)
		}
		if _, err := auth.Verify("+8613800138000", "246810"); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	clock.Advance(15 * time.Minute)
	if _, err := auth.Authenticate("unknown"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate error = %v, want ErrUnauthenticated", err)
	}
	if got := len(auth.sessions); got != 0 {
		t.Fatalf("expired session count = %d, want 0", got)
	}
}

func TestVerifyCleansExpiredSessionsBeforeSigning(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("verify session cleanup"), clock.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request first code: %v", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); err != nil {
		t.Fatalf("verify first code: %v", err)
	}
	clock.Advance(15 * time.Minute)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request second code: %v", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); err != nil {
		t.Fatalf("verify second code: %v", err)
	}
	if got := len(auth.sessions); got != 1 {
		t.Fatalf("session count after signing cleanup = %d, want 1", got)
	}
}

func TestVerifyRejectsFullSessionCapacityWithoutConsumingChallenge(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("session capacity"), clock.Now)
	tokens := make([]string, 0, authStateCapacity)
	for index := range authStateCapacity {
		if err := auth.RequestCode("+8613800138000"); err != nil {
			t.Fatalf("request code %d: %v", index, err)
		}
		session, err := auth.Verify("+8613800138000", "246810")
		if err != nil {
			t.Fatalf("verify %d: %v", index, err)
		}
		tokens = append(tokens, session.Token)
	}
	clock.Advance(14*time.Minute + 56*time.Second)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request capacity challenge: %v", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("full-capacity Verify error = %v, want ErrCapacityExceeded", err)
	}
	if got := len(auth.sessions); got != authStateCapacity {
		t.Fatalf("session count = %d, want %d", got, authStateCapacity)
	}
	for index, token := range tokens {
		if _, err := auth.Authenticate(token); err != nil {
			t.Fatalf("active token %d rejected: %v", index, err)
		}
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("challenge was consumed after capacity error: %v", err)
	}

	clock.Advance(4 * time.Second)
	session, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("retry after session expiry: %v", err)
	}
	if _, err := auth.Authenticate(session.Token); err != nil {
		t.Fatalf("retried session token rejected: %v", err)
	}
}

type blockingIdentityStore struct {
	IdentityStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockingIdentityStore) Get(subjectID string) (Identity, bool, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.IdentityStore.Get(subjectID)
}

func TestVerifyStoreIODoesNotBlockRequestCode(t *testing.T) {
	memory := NewMemoryStore()
	auth := NewAuthenticator(memory, testMockSecret("nonblocking request"), time.Now)
	auth.store = &blockingIdentityStore{
		IdentityStore: memory,
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request verify code: %v", err)
	}
	verifyDone := make(chan error, 1)
	go func() {
		_, err := auth.Verify("+8613800138000", "246810")
		verifyDone <- err
	}()
	<-auth.store.(*blockingIdentityStore).entered

	requestDone := make(chan error, 1)
	go func() {
		requestDone <- auth.RequestCode("+14155552671")
	}()
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatalf("concurrent RequestCode: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(auth.store.(*blockingIdentityStore).release)
		t.Fatal("RequestCode blocked behind Verify store I/O")
	}
	close(auth.store.(*blockingIdentityStore).release)
	if err := <-verifyDone; err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyStoreIODoesNotBlockAuthenticate(t *testing.T) {
	memory := NewMemoryStore()
	auth := NewAuthenticator(memory, testMockSecret("nonblocking authenticate"), time.Now)
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request first code: %v", err)
	}
	first, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatalf("first verify: %v", err)
	}
	blocking := &blockingIdentityStore{
		IdentityStore: memory,
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	auth.store = blocking
	if err := auth.RequestCode("+14155552671"); err != nil {
		t.Fatalf("request blocked verify code: %v", err)
	}
	verifyDone := make(chan error, 1)
	go func() {
		_, err := auth.Verify("+14155552671", "246810")
		verifyDone <- err
	}()
	<-blocking.entered

	authenticateDone := make(chan error, 1)
	go func() {
		_, err := auth.Authenticate(first.Token)
		authenticateDone <- err
	}()
	select {
	case err := <-authenticateDone:
		if err != nil {
			t.Fatalf("concurrent Authenticate: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		close(blocking.release)
		t.Fatal("Authenticate blocked behind Verify store I/O")
	}
	close(blocking.release)
	if err := <-verifyDone; err != nil {
		t.Fatalf("blocked verify: %v", err)
	}
}

type reentrantIdentityStore struct {
	IdentityStore
	onGet func()
}

func (s *reentrantIdentityStore) Get(subjectID string) (Identity, bool, error) {
	s.onGet()
	return s.IdentityStore.Get(subjectID)
}

func TestVerifyAllowsStoreToReenterAuthenticator(t *testing.T) {
	memory := NewMemoryStore()
	auth := NewAuthenticator(memory, testMockSecret("reentrant store"), time.Now)
	auth.store = &reentrantIdentityStore{
		IdentityStore: memory,
		onGet: func() {
			if err := auth.RequestCode("+14155552671"); err != nil {
				t.Errorf("reentrant RequestCode: %v", err)
			}
		},
	}
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request verify code: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := auth.Verify("+8613800138000", "246810")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Verify deadlocked when store reentered authenticator")
	}
}

type failOnceIdentityStore struct {
	IdentityStore
	err error
}

func (s *failOnceIdentityStore) Get(subjectID string) (Identity, bool, error) {
	if s.err != nil {
		err := s.err
		s.err = nil
		return Identity{}, false, err
	}
	return s.IdentityStore.Get(subjectID)
}

func TestVerifyReleasesCapacityReservationAfterStoreError(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC)
	memory := NewMemoryStore()
	injected := errors.New("injected store failure")
	auth := NewAuthenticator(memory, testMockSecret("reservation error"), func() time.Time { return now })
	auth.sessions = make([]storedSession, authStateCapacity-1)
	for index := range auth.sessions {
		auth.sessions[index].expiresAt = now.Add(time.Hour)
	}
	auth.store = &failOnceIdentityStore{IdentityStore: memory, err: injected}
	if err := auth.RequestCode("+8613800138000"); err != nil {
		t.Fatalf("request failing code: %v", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, injected) {
		t.Fatalf("Verify error = %v, want injected store error", err)
	}
	if _, err := auth.Verify("+8613800138000", "246810"); !errors.Is(err, ErrCodeNotRequested) {
		t.Fatalf("challenge after store I/O error = %v, want ErrCodeNotRequested", err)
	}
	if err := auth.RequestCode("+14155552671"); err != nil {
		t.Fatalf("request retry code: %v", err)
	}
	if _, err := auth.Verify("+14155552671", "246810"); err != nil {
		t.Fatalf("reservation was not released: %v", err)
	}
}

func TestConcurrentVerifyReservationsNeverExceedCapacity(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 30, 0, 0, time.UTC)
	auth := NewAuthenticator(NewMemoryStore(), testMockSecret("concurrent reservations"), func() time.Time { return now })
	auth.sessions = make([]storedSession, authStateCapacity-1)
	for index := range auth.sessions {
		auth.sessions[index].expiresAt = now.Add(time.Hour)
	}
	const attempts = 16
	phones := make([]string, attempts)
	for index := range attempts {
		phones[index] = fmt.Sprintf("+1415555%04d", index)
		if err := auth.RequestCode(phones[index]); err != nil {
			t.Fatalf("request code %d: %v", index, err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for _, phone := range phones {
		wg.Add(1)
		go func(phone string) {
			defer wg.Done()
			<-start
			_, err := auth.Verify(phone, "246810")
			results <- err
		}(phone)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	capacityErrors := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCapacityExceeded):
			capacityErrors++
		default:
			t.Fatalf("unexpected Verify error: %v", err)
		}
	}
	if successes != 1 || capacityErrors != attempts-1 {
		t.Fatalf("results = %d successes, %d capacity errors; want 1 and %d", successes, capacityErrors, attempts-1)
	}
	if got := len(auth.sessions); got != authStateCapacity {
		t.Fatalf("session count = %d, want %d", got, authStateCapacity)
	}
}

func TestConcurrentVerifyPreservesOneIdentity(t *testing.T) {
	store := NewMemoryStore()
	secret := testMockSecret("shared concurrent secret")
	base := time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	const count = 32
	identities := make(chan Identity, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup

	for index := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			auth := NewAuthenticator(store, secret, func() time.Time {
				return base.Add(time.Duration(index) * time.Second)
			})
			if err := auth.RequestCode("+8613800138000"); err != nil {
				errs <- err
				return
			}
			session, err := auth.Verify("+8613800138000", "246810")
			if err != nil {
				errs <- err
				return
			}
			identities <- session.Identity
		}(index)
	}
	wg.Wait()
	close(errs)
	close(identities)
	for err := range errs {
		t.Fatalf("concurrent verify: %v", err)
	}
	var first Identity
	for identity := range identities {
		if first.SubjectID == "" {
			first = identity
			continue
		}
		if identity != first {
			t.Fatalf("identity = %#v, want stable %#v", identity, first)
		}
	}
}

func stringsContain(value, secret string) bool {
	return len(secret) > 0 && regexp.MustCompile(regexp.QuoteMeta(secret)).MatchString(value)
}
