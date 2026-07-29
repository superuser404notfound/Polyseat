package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests exist because this package is the one thing between the network
// and a daemon that runs as root, and because none of it can be checked by
// looking at a running system: a session that is accepted when it should not be
// looks exactly like one that is accepted correctly.

// newStore returns a store somebody has already claimed, which is what most of
// these tests are about.
func newStore(t *testing.T) *Store {
	t.Helper()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := store.Claim("admin", "the first password"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	return store
}

// A machine nobody has claimed has no credentials, and says so, because that is
// what makes the interface offer to set one instead of asking for one.
func TestAFreshStoreIsUnclaimed(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if !store.NeedsSetup() {
		t.Fatal("a store with no credentials did not ask to be set up")
	}
}

// The failure this replaced a generated password with, and the one that would
// be silent: with no credentials stored, comparing nothing against nothing
// succeeds, so an empty form would have been a valid sign in.
func TestAnUnclaimedStoreLetsNobodyIn(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	for _, tc := range []struct{ user, password string }{
		{"", ""},
		{"admin", ""},
		{"", "anything"},
		{"admin", "anything"},
	} {
		if store.Check(tc.user, tc.password) {
			t.Errorf("Check(%q, %q) let somebody in before a password was set", tc.user, tc.password)
		}
	}
}

func TestClaimIsOnlyPossibleOnce(t *testing.T) {
	dir := t.TempDir()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := store.Claim("rooky", "the first password"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if store.NeedsSetup() {
		t.Error("the store still asks to be set up after being claimed")
	}

	// The one that matters. Without it, whoever reaches the page second takes
	// the machine from whoever reached it first.
	if err := store.Claim("somebody else", "another password"); err == nil {
		t.Error("a second claim was accepted, so the first password can be overwritten by anyone")
	}

	if !store.Check("rooky", "the first password") {
		t.Error("the first password stopped working")
	}

	// The file has to be readable by root only. It holds the session signing
	// key, so anybody who can read it can mint a session.
	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials are %o, want 600", perm)
	}

	// Reopening must not lose them, or every daemon restart would offer the
	// machine to whoever asked next.
	second, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}

	if second.NeedsSetup() {
		t.Error("a reopened store asks to be set up again")
	}

	if !second.Check("rooky", "the first password") {
		t.Error("the password did not survive a reopen")
	}
}

func TestCheck(t *testing.T) {
	store := newStore(t)

	if err := store.SetPassword("rooky", "correct horse"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	cases := []struct {
		name     string
		user     string
		password string
		want     bool
	}{
		{"both right", "rooky", "correct horse", true},
		{"wrong password", "rooky", "correct hors", false},
		{"wrong user", "admin", "correct horse", false},
		{"both wrong", "admin", "nope", false},
		{"empty password", "rooky", "", false},
		{"password as user name", "correct horse", "rooky", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := store.Check(tc.user, tc.password); got != tc.want {
				t.Errorf("Check(%q, %q) = %v, want %v", tc.user, tc.password, got, tc.want)
			}
		})
	}
}

func TestSetPasswordRejectsShortAndEmpty(t *testing.T) {
	store := newStore(t)

	if err := store.SetPassword("admin", strings.Repeat("a", MinPasswordLength-1)); err == nil {
		t.Error("a password below the minimum was accepted")
	}

	if err := store.SetPassword("", "long enough password"); err == nil {
		t.Error("an empty user name was accepted")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	store := newStore(t)

	token := store.Issue()
	if !store.Valid(token) {
		t.Fatal("a freshly issued token was rejected")
	}

	// Every token has to differ, or two people sharing a browser profile would
	// share a session.
	if store.Issue() == token {
		t.Error("two issued tokens are identical")
	}
}

func TestSessionRejectsTampering(t *testing.T) {
	store := newStore(t)

	token := store.Issue()
	payload, mac, _ := strings.Cut(token, ".")

	// Moving the expiry forward is the interesting one: it is what somebody
	// with an expired session would try, and it only fails because the
	// signature covers the expiry rather than just the nonce.
	_, nonce, _ := strings.Cut(payload, ":")
	far := strconv.FormatInt(time.Now().Add(10*365*24*time.Hour).Unix(), 10)

	cases := map[string]string{
		"no separator":     payload + mac,
		"empty":            "",
		"payload only":     payload,
		"signature only":   "." + mac,
		"expiry moved":     far + ":" + nonce + "." + mac,
		"nonce changed":    strings.Split(payload, ":")[0] + ":AAAA." + mac,
		"signature broken": payload + "." + mac[:len(mac)-2] + "AA",
		"foreign token":    "9999999999:AAAA.BBBB",
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if store.Valid(token) {
				t.Errorf("accepted a tampered token: %q", token)
			}
		})
	}
}

func TestSessionExpires(t *testing.T) {
	store := newStore(t)

	store.mu.RLock()
	key := store.creds.SessionKey
	store.mu.RUnlock()

	// Signed properly, but for a moment that has passed.
	payload := strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10) + ":" + nonce()

	if store.Valid(payload + "." + sign(key, payload)) {
		t.Error("an expired token was accepted")
	}
}

func TestChangingThePasswordEndsSessions(t *testing.T) {
	store := newStore(t)

	token := store.Issue()
	if !store.Valid(token) {
		t.Fatal("the token was invalid before the change")
	}

	if err := store.SetPassword("admin", "a different password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	if store.Valid(token) {
		t.Error("a session survived a password change")
	}

	if !store.Valid(store.Issue()) {
		t.Error("no new session could be issued after the change")
	}
}

func TestLimiter(t *testing.T) {
	store := newStore(t)

	const source = "10.0.0.1"

	for i := range freeAttempts {
		if ok, _ := store.Allow(source); !ok {
			t.Fatalf("attempt %d was blocked while it should still be free", i+1)
		}

		store.Failed(source)
	}

	// The attempt after the free ones is what has to start waiting.
	store.Failed(source)

	ok, wait := store.Allow(source)
	if ok {
		t.Fatal("the limiter never engaged")
	}

	if wait <= 0 {
		t.Errorf("blocked without a wait: %v", wait)
	}

	// A different source must not inherit the block, or one noisy client would
	// lock out the whole household.
	if ok, _ := store.Allow("10.0.0.2"); !ok {
		t.Error("an unrelated source was blocked")
	}

	// Succeeding clears the record, so somebody who mistyped twice and then got
	// it right is not still waiting.
	store.Succeeded(source)

	if ok, _ := store.Allow(source); !ok {
		t.Error("a successful login did not clear the block")
	}
}

func TestStoredCredentialsCarryNoPlaintext(t *testing.T) {
	dir := t.TempDir()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const password = "a memorable password"

	if err := store.SetPassword("admin", password); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if strings.Contains(string(data), password) {
		t.Fatal("the password is stored in plain text")
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if creds.Algorithm != "argon2id" {
		t.Errorf("algorithm is %q, want argon2id", creds.Algorithm)
	}

	if len(creds.Salt) != saltLen || len(creds.Hash) != argonKeyLen {
		t.Errorf("salt %d bytes, hash %d bytes", len(creds.Salt), len(creds.Hash))
	}

	if len(creds.SessionKey) != 32 {
		t.Errorf("session key is %d bytes, want 32", len(creds.SessionKey))
	}
}

func TestSaltAndKeyDifferPerPassword(t *testing.T) {
	store := newStore(t)

	if err := store.SetPassword("admin", "the same password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	store.mu.RLock()
	first := store.creds
	store.mu.RUnlock()

	if err := store.SetPassword("admin", "the same password"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	store.mu.RLock()
	second := store.creds
	store.mu.RUnlock()

	// Same password, different salt, therefore a different hash. Without this
	// two seats or two machines with the same password would be visibly the
	// same in their stored credentials.
	if string(first.Salt) == string(second.Salt) {
		t.Error("the salt was reused")
	}

	if string(first.Hash) == string(second.Hash) {
		t.Error("the hash is the same for the same password, so it is unsalted")
	}

	if string(first.SessionKey) == string(second.SessionKey) {
		t.Error("the session key was not rotated")
	}
}
