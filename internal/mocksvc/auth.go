package mocksvc

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	ErrInvalidPhone     = errors.New("invalid phone")
	ErrCodeNotRequested = errors.New("code not requested")
	ErrInvalidCode      = errors.New("invalid code")
	ErrCodeExpired      = errors.New("code expired")
	ErrTooManyAttempts  = errors.New("too many attempts")
	ErrUnauthenticated  = errors.New("unauthenticated")
	ErrInvalidSecret    = errors.New("invalid mock secret")
	ErrCapacityExceeded = errors.New("capacity exceeded")
)

type AuthSession struct {
	Token     string    `json:"token"`
	Identity  Identity  `json:"identity"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Clock func() time.Time

type challenge struct {
	expiresAt time.Time
	attempts  int
}

type storedSession struct {
	tokenDigest [sha256.Size]byte
	identity    Identity
	expiresAt   time.Time
}

const authStateCapacity = 1024

type Authenticator struct {
	store           IdentityStore
	identitySecret  []byte
	challengeSecret []byte
	clock           Clock
	initErr         error
	scenario        string

	mu         sync.Mutex
	challenges map[[sha256.Size]byte]challenge
	sessions   []storedSession
	reserved   int
}

func NewAuthenticator(store IdentityStore, mockSecret []byte, clock Clock) *Authenticator {
	return NewAuthenticatorForScenario(store, mockSecret, clock, "success")
}

func NewAuthenticatorForScenario(store IdentityStore, mockSecret []byte, clock Clock, scenario string) *Authenticator {
	return NewAuthenticatorWithChallengeSecret(store, mockSecret, mockSecret, clock, scenario)
}

func NewAuthenticatorWithChallengeSecret(
	store IdentityStore,
	identitySecret []byte,
	challengeSecret []byte,
	clock Clock,
	scenario string,
) *Authenticator {
	if clock == nil {
		clock = time.Now
	}
	authenticator := &Authenticator{
		store:           store,
		identitySecret:  append([]byte(nil), identitySecret...),
		challengeSecret: append([]byte(nil), challengeSecret...),
		clock:           clock,
		scenario:        scenario,
		challenges:      make(map[[sha256.Size]byte]challenge),
	}
	if len(identitySecret) < sha256.Size || len(challengeSecret) < sha256.Size {
		authenticator.initErr = ErrInvalidSecret
	}
	return authenticator
}

func (a *Authenticator) RequestCode(phone string) error {
	if a.initErr != nil {
		return a.initErr
	}
	normalized, err := normalizePhone(phone)
	if err != nil {
		return err
	}
	key := a.challengeKey(normalized)
	now := a.clock()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanExpiredChallengesLocked(now)
	if _, exists := a.challenges[key]; !exists && len(a.challenges) >= authStateCapacity {
		return ErrCapacityExceeded
	}
	a.challenges[key] = challenge{expiresAt: now.Add(5 * time.Minute)}
	return nil
}

func (a *Authenticator) Verify(phone, code string) (AuthSession, error) {
	if a.initErr != nil {
		return AuthSession{}, a.initErr
	}
	normalized, err := normalizePhone(phone)
	if err != nil {
		return AuthSession{}, err
	}
	key := a.challengeKey(normalized)
	now := a.clock()

	a.mu.Lock()
	pending, ok := a.challenges[key]
	expired := ok && !now.Before(pending.expiresAt)
	a.cleanExpiredChallengesLocked(now)
	if expired {
		a.mu.Unlock()
		return AuthSession{}, ErrCodeExpired
	}
	if !ok {
		a.mu.Unlock()
		return AuthSession{}, ErrCodeNotRequested
	}
	if pending.attempts >= 5 {
		delete(a.challenges, key)
		a.mu.Unlock()
		return AuthSession{}, ErrTooManyAttempts
	}
	if code != "246810" || a.scenario == "otp_error" {
		pending.attempts++
		a.challenges[key] = pending
		a.mu.Unlock()
		return AuthSession{}, ErrInvalidCode
	}
	a.cleanExpiredSessionsLocked(now)
	if len(a.sessions)+a.reserved >= authStateCapacity {
		a.mu.Unlock()
		return AuthSession{}, ErrCapacityExceeded
	}
	a.reserved++
	delete(a.challenges, key)
	a.mu.Unlock()

	subjectID := deriveSubjectID(a.identitySecret, normalized)
	identity, exists, err := a.store.Get(subjectID)
	if err != nil {
		a.releaseSessionReservation()
		return AuthSession{}, err
	}
	if !exists {
		identity = Identity{
			SubjectID: subjectID,
			KuAIID:    deriveKuAIID(a.identitySecret, subjectID),
			CreatedAt: a.clock(),
		}
		identity, err = a.store.GetOrPut(identity)
		if err != nil {
			a.releaseSessionReservation()
			return AuthSession{}, err
		}
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		a.releaseSessionReservation()
		return AuthSession{}, ErrUnauthenticated
	}
	session := AuthSession{
		Token:     base64.RawURLEncoding.EncodeToString(tokenBytes),
		Identity:  identity,
		ExpiresAt: a.clock().Add(15 * time.Minute),
	}
	a.mu.Lock()
	a.reserved--
	a.sessions = append(a.sessions, storedSession{
		tokenDigest: sha256.Sum256([]byte(session.Token)),
		identity:    session.Identity,
		expiresAt:   session.ExpiresAt,
	})
	a.mu.Unlock()
	return session, nil
}

func (a *Authenticator) Authenticate(token string) (Identity, error) {
	if a.initErr != nil {
		return Identity{}, a.initErr
	}
	now := a.clock()
	tokenDigest := sha256.Sum256([]byte(token))
	a.mu.Lock()
	defer a.mu.Unlock()

	a.cleanExpiredSessionsLocked(now)
	var matched Identity
	found := 0
	for _, session := range a.sessions {
		equal := subtle.ConstantTimeCompare(session.tokenDigest[:], tokenDigest[:])
		if equal == 1 {
			matched = session.identity
		}
		found |= equal
	}
	if found != 1 {
		return Identity{}, ErrUnauthenticated
	}
	return matched, nil
}

func (a *Authenticator) challengeKey(normalizedPhone string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, a.challengeSecret)
	_, _ = mac.Write([]byte("challenge:" + normalizedPhone))
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func (a *Authenticator) cleanExpiredChallengesLocked(now time.Time) {
	for key, pending := range a.challenges {
		if !now.Before(pending.expiresAt) {
			delete(a.challenges, key)
		}
	}
}

func (a *Authenticator) cleanExpiredSessionsLocked(now time.Time) {
	active := a.sessions[:0]
	for _, session := range a.sessions {
		if now.Before(session.expiresAt) {
			active = append(active, session)
		}
	}
	for index := len(active); index < len(a.sessions); index++ {
		a.sessions[index] = storedSession{}
	}
	a.sessions = active
}

func (a *Authenticator) releaseSessionReservation() {
	a.mu.Lock()
	a.reserved--
	a.mu.Unlock()
}

func normalizePhone(phone string) (string, error) {
	var builder strings.Builder
	hadPlus := false
	for index, char := range strings.TrimSpace(phone) {
		switch {
		case unicode.IsDigit(char):
			if char > unicode.MaxASCII {
				return "", ErrInvalidPhone
			}
			builder.WriteRune(char)
		case char == '+' && index == 0:
			hadPlus = true
		case unicode.IsSpace(char), char == '-', char == '(', char == ')':
		default:
			return "", ErrInvalidPhone
		}
	}
	digits := builder.String()
	if hadPlus {
		if len(digits) < 8 || len(digits) > 15 || digits[0] == '0' {
			return "", ErrInvalidPhone
		}
		return "+" + digits, nil
	}
	if !isCanonicalChinaMobile(digits) {
		return "", ErrInvalidPhone
	}
	return "+86" + digits, nil
}

func isCanonicalChinaMobile(digits string) bool {
	return len(digits) == 11 && digits[0] == '1' && digits[1] >= '3' && digits[1] <= '9'
}

func deriveSubjectID(secret []byte, normalizedPhone string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("subject:" + normalizedPhone))
	return "sub-" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func deriveKuAIID(secret []byte, subjectID string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("kuai-id:" + subjectID))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return "KUAI-" + encoded[:10]
}
