// Package adminops implements isolated operator authentication, VictoriaMetrics/Grafana telemetry
// aggregation, and Postgres/file-backed self-evolution candidate governance for #/admin.
package adminops

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 16

	adminIdleTTL     = 12 * time.Hour
	adminMaxTTL      = 24 * time.Hour
	rateWindow       = 5 * time.Minute
	maxFailedPerIP   = 8
	AdminCookieName  = "cw_admin"
)

var (
	ErrTooManyAttempts = errors.New("too many failed login attempts")
	ErrInvalidHash     = errors.New("invalid password hash format")
)

// HashPassword computes a salted scrypt hash formatted as scrypt$N$r$p$saltB64$hashB64.
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("admin password must be at least 8 characters")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s",
		scryptN, scryptR, scryptP,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk),
	), nil
}

// VerifyPassword verifies a plaintext password against a scrypt$... hash in constant time.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false
	}
	n, err1 := strconv.Atoi(parts[1])
	r, err2 := strconv.Atoi(parts[2])
	p, err3 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil || err3 != nil || n < 1024 || n > 1<<20 || r < 1 || r > 32 || p < 1 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false
	}
	got, err := scrypt.Key([]byte(password), salt, n, r, p, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

type AuthConfig struct {
	Email        string
	PasswordHash string
	AllowedUsers map[string]bool
	Now          func() time.Time
}

type AdminSession struct {
	ID        string    `json:"-"`
	Email     string    `json:"email"`
	Kind      string    `json:"kind"` // "password" | "user_rbac"
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AuthManager struct {
	email        string
	passwordHash string
	allowedUsers map[string]bool
	now          func() time.Time

	mu       sync.Mutex
	sessions map[string]AdminSession
	failures map[string][]time.Time
}

func NewAuthManager(cfg AuthConfig) *AuthManager {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	users := map[string]bool{}
	for k, v := range cfg.AllowedUsers {
		clean := strings.TrimSpace(strings.ToLower(k))
		if clean != "" && v {
			users[clean] = true
		}
	}
	return &AuthManager{
		email:        strings.TrimSpace(strings.ToLower(cfg.Email)),
		passwordHash: strings.TrimSpace(cfg.PasswordHash),
		allowedUsers: users,
		now:          now,
		sessions:     map[string]AdminSession{},
		failures:     map[string][]time.Time{},
	}
}

func (a *AuthManager) PasswordEnabled() bool {
	return a != nil && a.email != "" && a.passwordHash != ""
}

func (a *AuthManager) Enabled() bool {
	return a != nil && (a.PasswordEnabled() || len(a.allowedUsers) > 0)
}

func (a *AuthManager) IsAllowedUser(userID, loginEmail string) bool {
	if a == nil || len(a.allowedUsers) == 0 {
		return false
	}
	if u := strings.TrimSpace(strings.ToLower(userID)); u != "" && a.allowedUsers[u] {
		return true
	}
	if e := strings.TrimSpace(strings.ToLower(loginEmail)); e != "" && a.allowedUsers[e] {
		return true
	}
	return false
}

// Login checks rate limits and credentials, creating a server-side admin session on success.
func (a *AuthManager) Login(clientIP, email, password string) (AdminSession, error) {
	if !a.PasswordEnabled() {
		return AdminSession{}, errors.New("admin password auth disabled")
	}
	ip := strings.TrimSpace(clientIP)
	if ip == "" {
		ip = "unknown"
	}
	now := a.now().UTC()

	a.mu.Lock()
	recent := a.pruneFailuresLocked(ip, now)
	if len(recent) >= maxFailedPerIP {
		a.mu.Unlock()
		return AdminSession{}, ErrTooManyAttempts
	}
	a.mu.Unlock()

	givenEmail := strings.TrimSpace(strings.ToLower(email))
	emailMatch := subtle.ConstantTimeCompare([]byte(givenEmail), []byte(a.email)) == 1
	passMatch := VerifyPassword(password, a.passwordHash)
	if !emailMatch || !passMatch {
		a.mu.Lock()
		a.failures[ip] = append(a.pruneFailuresLocked(ip, now), now)
		a.mu.Unlock()
		return AdminSession{}, errors.New("invalid credentials")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return AdminSession{}, err
	}
	sess := AdminSession{
		ID:        hex.EncodeToString(raw),
		Email:     a.email,
		Kind:      "password",
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(adminMaxTTL),
	}
	a.mu.Lock()
	delete(a.failures, ip)
	a.sessions[sess.ID] = sess
	a.mu.Unlock()
	return sess, nil
}

func (a *AuthManager) pruneFailuresLocked(ip string, now time.Time) []time.Time {
	cutoff := now.Add(-rateWindow)
	var kept []time.Time
	for _, ts := range a.failures[ip] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(a.failures, ip)
	} else {
		a.failures[ip] = kept
	}
	return kept
}

func (a *AuthManager) LookupSession(id string) (AdminSession, bool) {
	if a == nil || strings.TrimSpace(id) == "" {
		return AdminSession{}, false
	}
	now := a.now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	sess, ok := a.sessions[id]
	if !ok {
		return AdminSession{}, false
	}
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeen) > adminIdleTTL {
		delete(a.sessions, id)
		return AdminSession{}, false
	}
	sess.LastSeen = now
	a.sessions[id] = sess
	return sess, true
}

func (a *AuthManager) RevokeSession(id string) {
	if a == nil || id == "" {
		return
	}
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}
