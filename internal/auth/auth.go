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

	// writeMu makes deciding on new credentials, hashing them and writing them
	// one step. mu cannot do that job: it is held for reads by every request
	// that checks a session, and holding it across a hash that takes a tenth of
	// a second would stall the whole interface for that long.
	writeMu sync.Mutex

	limiter *limiter
}

// Open loads the credentials, creating them on first run.
//
// A machine nobody has claimed yet has no credentials at all, and the interface
// asks for a password to be chosen instead of asking for one to be typed. That
// is a deliberate trade and it replaced the opposite one: the first version
// generated a password and wrote it to the log, so that the window in which
// anybody could claim the daemon was never open.
//
// What the generated password cost was the one thing Polyseat is supposed not
// to need, a terminal. Reading it back meant journalctl, on a machine whose
// whole point is that it is driven from a browser and a gamepad. Sunshine makes
// the same trade for the same reason.
//
// So the window exists, and it is closed by the first person to open the page.
// This is a tool for a household's own machine on its own network; it is not
// one to hand to the internet, which the documentation says in as many words.
func Open(stateDir string) (*Store, error) {
	path := filepath.Join(stateDir, "credentials.json")

	s := &Store{path: path, limiter: newLimiter()}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.creds); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		return s, nil

	case os.IsNotExist(err):
		return s, nil

	default:
		return nil, err
	}
}

// NeedsSetup reports whether nobody has chosen a password yet.
func (s *Store) NeedsSetup() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.creds.Hash) == 0
}

// Claim sets the first credentials, and only the first.
//
// Separate from SetPassword because the check and the write have to be one
// step. Two browsers opening an unclaimed daemon at the same moment would
// otherwise both find it unclaimed, both set a password, and the second would
// win silently.
func (s *Store) Claim(username, password string) error {
	// Held across the hash and the write, not only across the look. The first
	// version released its lock between finding the machine unclaimed and
	// setting the password, so two requests arriving together both found it
	// unclaimed, both hashed, and both wrote: the second password won without
	// either browser being told, and the two writes shared one temporary file
	// name, which can leave a credentials.json the daemon cannot parse on its
	// next start.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if !s.NeedsSetup() {
		return errors.New("this machine already has a password")
	}

	return s.setPassword(username, password)
}

// Username is who logs in.
func (s *Store) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.creds.Username
}

// SetPassword replaces the credentials and ends every existing session.
func (s *Store) SetPassword(username, password string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.setPassword(username, password)
}

// setPassword is SetPassword for a caller that already holds writeMu.
func (s *Store) setPassword(username, password string) error {
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
		Hash:       hash([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen),
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

	// A name of its own for every write rather than one fixed .tmp beside the
	// file. writeMu already keeps this process to one write at a time; the
	// unique name is what keeps that from being the only thing standing
	// between two writers and a file made of both of them. CreateTemp makes it
	// 0600, which is what the finished file has to be: it holds the key that
	// signs sessions.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), s.path); err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	return nil
}

// Check verifies a user name and password.
func (s *Store) Check(username, password string) bool {
	s.mu.RLock()
	creds := s.creds
	s.mu.RUnlock()

	// An unclaimed machine has no credentials, and comparing nothing with
	// nothing succeeds: both the name and the hash would be empty on each side
	// and the constant time compare would say yes. Signing in with a blank form
	// is not what "nobody has set a password yet" is supposed to mean.
	if len(creds.Hash) == 0 {
		return false
	}

	// Hash regardless, so a wrong user name does not answer faster than a
	// wrong password and give away which of the two was right.
	got := hash([]byte(password), creds.Salt, creds.Time, creds.Memory, creds.Threads, uint32(len(creds.Hash)))

	nameOK := subtle.ConstantTimeCompare([]byte(username), []byte(creds.Username)) == 1
	hashOK := subtle.ConstantTimeCompare(got, creds.Hash) == 1

	return nameOK && hashOK
}

// hashSlots is how many argon2 hashes may run at once, across every caller.
//
// Each one allocates 64 MiB, and nothing else bounds how many run: the limiter
// counts per address, a login form is reachable without a session, and a burst
// of parallel requests from a handful of addresses asked this daemon for a
// gigabyte at a time. Two is enough for a household, where two people typing a
// password in the same tenth of a second is already unusual; the rest wait
// their turn rather than being refused, because a refusal would lock the
// owner out for as long as somebody else keeps knocking.
var hashSlots = make(chan struct{}, 2)

// idKey is argon2.IDKey, as a variable so a test can count how many run at once.
var idKey = argon2.IDKey

// hash is argon2id behind hashSlots.
func hash(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()

	return idKey(password, salt, iterations, memory, threads, keyLen)
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

	// An unclaimed machine has no key, and an HMAC under an empty key is one
	// anybody can compute: without this, a token signed with nothing was a
	// valid session on every machine nobody had claimed yet, which reached
	// every guarded endpoint before the first password was even chosen.
	if len(key) == 0 {
		return false
	}

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

// ------------------------------------------------------------------- limiter

// failWindow is how long failed attempts are remembered.
const failWindow = 15 * time.Minute

// freeAttempts is how many failures cost nothing. Beyond it every further
// attempt has to wait, doubling each time up to a cap.
const freeAttempts = 5

// maxSources is how many addresses the limiter remembers at once.
//
// Without a bound the map grew by one entry for every address that ever got a
// password wrong and shrank only when that same address came back, so anybody
// able to vary their source address could grow it for as long as they liked.
// Four thousand is far past what a household produces in fifteen minutes and
// small enough that sweeping it costs nothing.
const maxSources = 4096

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

// bucket is the key an address is counted under.
//
// An IPv6 address is counted by its /64. That is the smallest block a network
// is handed, and every host on it chooses its own low 64 bits, privacy
// addresses included, so counting single addresses gave one machine as many
// fresh budgets as it cared to generate.
func bucket(source string) string {
	ip := net.ParseIP(source)
	if ip == nil || ip.To4() != nil {
		return source
	}

	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Allow reports whether a source may try, and if not, how long it has to wait.
// It records nothing; Attempt is what a caller about to check a password uses.
//
// argon2id already caps guessing at a few attempts a second per core, which is
// most of the protection. This exists so that a run at it also stops being
// free after a handful of tries, and so the log has something to say.
func (s *Store) Allow(source string) (bool, time.Duration) {
	l := s.limiter

	l.mu.Lock()
	defer l.mu.Unlock()

	return l.allow(bucket(source))
}

func (l *limiter) allow(key string) (bool, time.Duration) {
	a, ok := l.by[key]
	if !ok {
		return true, 0
	}

	if time.Since(a.last) > failWindow {
		delete(l.by, key)

		return true, 0
	}

	if wait := time.Until(a.until); wait > 0 {
		return false, wait
	}

	return true, 0
}

// Attempt is Allow and Failed in one step: it answers whether a source may
// try, and if it may, counts the try as a failure before the password has been
// looked at. A correct password then clears it with Succeeded.
//
// Counted in advance because counting afterwards counted nothing that was
// still running. The hash takes a tenth of a second, and every request that
// arrived inside it found the same clean record, so a burst of parallel
// guesses all went through before the first of them had been written down.
func (s *Store) Attempt(source string) (bool, time.Duration) {
	l := s.limiter
	key := bucket(source)

	l.mu.Lock()
	defer l.mu.Unlock()

	ok, wait := l.allow(key)
	if ok {
		l.failed(key)
	}

	return ok, wait
}

// Failed records a failed attempt.
func (s *Store) Failed(source string) {
	l := s.limiter

	l.mu.Lock()
	defer l.mu.Unlock()

	l.failed(bucket(source))
}

func (l *limiter) failed(key string) {
	a, ok := l.by[key]
	if !ok || time.Since(a.last) > failWindow {
		if !ok {
			l.makeRoom()
		}

		a = &attempts{}
		l.by[key] = a
	}

	a.count++
	a.last = time.Now()

	if a.count > freeAttempts {
		delay := time.Duration(1<<min(a.count-freeAttempts, 6)) * time.Second
		a.until = time.Now().Add(delay)
	}
}

// makeRoom keeps the map under maxSources before a new source is added.
//
// Expired records go first, which on any real network is all it ever has to
// do. Only a map still full of live records loses the one heard from longest
// ago. That does let somebody with thousands of addresses push an older record
// out, and the alternative, refusing sources the map has no room for, would
// let the same somebody lock out everybody else instead, which is worse.
func (l *limiter) makeRoom() {
	if len(l.by) < maxSources {
		return
	}

	var oldest string

	for key, a := range l.by {
		if time.Since(a.last) > failWindow {
			delete(l.by, key)

			continue
		}

		if oldest == "" || a.last.Before(l.by[oldest].last) {
			oldest = key
		}
	}

	if len(l.by) >= maxSources && oldest != "" {
		delete(l.by, oldest)
	}
}

// Succeeded clears the record for a source.
func (s *Store) Succeeded(source string) {
	l := s.limiter

	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.by, bucket(source))
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
