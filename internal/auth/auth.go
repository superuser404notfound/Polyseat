// Package auth guards the web interface with a password.
//
// The daemon serves an interface that can create and destroy containers as
// root, and it is meant to be reachable from the couch, which means from the
// network. Those two together leave no room for an open door. Sunshine solves
// the same problem the same way, and the seats already make people click
// through a self-signed certificate once, so this matches what they know.
//
// Three pieces: a password stored as an argon2id hash, a signed session cookie,
// and a limiter so a password on the LAN cannot be guessed at machine speed.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// SessionTTL is how long a login lasts. Long, because this is a machine in
// somebody's home and being asked for a password every day to look at a seat
// list teaches people to pick a shorter password.
const SessionTTL = 30 * 24 * time.Hour

// CookieName is the session cookie.
const CookieName = "polyseat_session"

// MinPasswordLength is enforced because the interface is meant to be reachable
// from the network. A password that guards a root daemon over the LAN is not
// the place to be accommodating.
const MinPasswordLength = 8

// argon2id parameters. Memory hard on purpose: the alternative, a fast hash
// with many iterations, is exactly what a GPU is good at.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB, so 64 MiB per attempt
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// Credentials is what is written to disk.
type Credentials struct {
	Username  string    `json:"username"`
	Algorithm string    `json:"algorithm"`
	Salt      []byte    `json:"salt"`
	Hash      []byte    `json:"hash"`
	Time      uint32    `json:"time"`
	Memory    uint32    `json:"memory"`
	Threads   uint8     `json:"threads"`
	Updated   time.Time `json:"updated"`

	// SessionKey signs session cookies. Rotating it on a password change is
	// what makes every existing session end, which is the behaviour people
	// expect from changing a password and would otherwise not get.
	SessionKey []byte `json:"session_key"`
}

// Store holds the credentials and issues sessions.
type Store struct {
	path string

	mu    sync.RWMutex
	creds Credentials

	limiter *limiter
}

// Open loads the credentials, creating them on first run.
//
// The initial password is generated rather than left empty. A daemon that
// accepts anything until somebody sets a password has an open window, and
// whoever reaches it first wins; on a machine that listens on the network that
// window is not acceptable. The generated password is returned so the caller
// can log it once, which puts it behind the same wall as root.
func Open(stateDir string) (store *Store, initialPassword string, err error) {
	path := filepath.Join(stateDir, "credentials.json")

	s := &Store{path: path, limiter: newLimiter()}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.creds); err != nil {
			return nil, "", fmt.Errorf("%s: %w", path, err)
		}

		return s, "", nil

	case os.IsNotExist(err):
		password, err := randomPassword()
		if err != nil {
			return nil, "", err
		}

		if err := s.SetPassword("admin", password); err != nil {
			return nil, "", err
		}

		return s, password, nil

	default:
		return nil, "", err
	}
}

// Username is who logs in.
func (s *Store) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.creds.Username
}

// SetPassword replaces the credentials and ends every existing session.
func (s *Store) SetPassword(username, password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("the password has to be at least %d characters", MinPasswordLength)
	}

	if username == "" {
		return errors.New("the user name cannot be empty")
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}

	sessionKey := make([]byte, 32)
	if _, err := rand.Read(sessionKey); err != nil {
		return err
	}

	creds := Credentials{
		Username:   username,
		Algorithm:  "argon2id",
		Salt:       salt,
		Hash:       argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen),
		Time:       argonTime,
		Memory:     argonMemory,
		Threads:    argonThreads,
		Updated:    time.Now(),
		SessionKey: sessionKey,
	}

	if err := s.write(creds); err != nil {
		return err
	}

	s.mu.Lock()
	s.creds = creds
	s.mu.Unlock()

	return nil
}

func (s *Store) write(creds Credentials) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, s.path)
}

// Check verifies a user name and password.
func (s *Store) Check(username, password string) bool {
	s.mu.RLock()
	creds := s.creds
	s.mu.RUnlock()

	// Hash regardless, so a wrong user name does not answer faster than a
	// wrong password and give away which of the two was right.
	hash := argon2.IDKey([]byte(password), creds.Salt, creds.Time, creds.Memory, creds.Threads, uint32(len(creds.Hash)))

	nameOK := subtle.ConstantTimeCompare([]byte(username), []byte(creds.Username)) == 1
	hashOK := subtle.ConstantTimeCompare(hash, creds.Hash) == 1

	return nameOK && hashOK
}

// ------------------------------------------------------------------ sessions

// Issue returns a signed session token.
//
// Signed rather than stored. There is no server side session table to keep,
// nothing to lose on a restart, and a daemon update therefore does not log
// everybody out. The cost is that a single session cannot be revoked on its
// own; changing the password ends all of them at once, which for a household
// machine is the operation people actually want.
func (s *Store) Issue() string {
	s.mu.RLock()
	key := s.creds.SessionKey
	s.mu.RUnlock()

	payload := strconv.FormatInt(time.Now().Add(SessionTTL).Unix(), 10) + ":" + nonce()

	return payload + "." + sign(key, payload)
}

// Valid reports whether a token is genuine and still current.
func (s *Store) Valid(token string) bool {
	payload, mac, found := strings.Cut(token, ".")
	if !found {
		return false
	}

	s.mu.RLock()
	key := s.creds.SessionKey
	s.mu.RUnlock()

	if !hmac.Equal([]byte(mac), []byte(sign(key, payload))) {
		return false
	}

	expiry, _, found := strings.Cut(payload, ":")
	if !found {
		return false
	}

	seconds, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return false
	}

	return time.Now().Unix() < seconds
}

func sign(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func nonce() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail on Linux, and a session token built from a
		// predictable value would be worse than no session at all.
		panic(err)
	}

	return base64.RawURLEncoding.EncodeToString(buf)
}

// randomPassword returns something readable enough to type in once from a
// journal line, without ambiguous characters.
func randomPassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}

	return string(out), nil
}

// ------------------------------------------------------------------- limiter

// failWindow is how long failed attempts are remembered.
const failWindow = 15 * time.Minute

// freeAttempts is how many failures cost nothing. Beyond it every further
// attempt has to wait, doubling each time up to a cap.
const freeAttempts = 5

type attempts struct {
	count int
	last  time.Time
	until time.Time
}

type limiter struct {
	mu sync.Mutex
	by map[string]*attempts
}

func newLimiter() *limiter {
	return &limiter{by: map[string]*attempts{}}
}

// Allow reports whether a source may try, and if not, how long it has to wait.
//
// argon2id already caps guessing at a few attempts a second per core, which is
// most of the protection. This exists so that a run at it also stops being
// free after a handful of tries, and so the log has something to say.
func (s *Store) Allow(source string) (bool, time.Duration) {
	l := s.limiter

	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.by[source]
	if !ok {
		return true, 0
	}

	if time.Since(a.last) > failWindow {
		delete(l.by, source)

		return true, 0
	}

	if wait := time.Until(a.until); wait > 0 {
		return false, wait
	}

	return true, 0
}

// Failed records a failed attempt.
func (s *Store) Failed(source string) {
	l := s.limiter

	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.by[source]
	if !ok || time.Since(a.last) > failWindow {
		a = &attempts{}
		l.by[source] = a
	}

	a.count++
	a.last = time.Now()

	if a.count > freeAttempts {
		delay := time.Duration(1<<min(a.count-freeAttempts, 6)) * time.Second
		a.until = time.Now().Add(delay)
	}
}

// Succeeded clears the record for a source.
func (s *Store) Succeeded(source string) {
	l := s.limiter

	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.by, source)
}

// Source identifies a caller for rate limiting. The address only, never a
// header a client controls.
func Source(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
