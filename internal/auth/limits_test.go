package auth

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// These are the ways the store could be made to misbehave by asking it many
// things at once, which a test making one request at a time never sees.

// Two browsers reaching an unclaimed machine together. The first version looked
// under a lock and set the password after letting go of it, so both requests
// found the machine unclaimed and both won, and the two writes shared a
// temporary file that could be left holding half of each.
func TestClaimsArrivingTogetherHaveOneWinner(t *testing.T) {
	dir := t.TempDir()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const racers = 6

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []string
	)

	start := make(chan struct{})

	for i := range racers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			name := "racer" + strconv.Itoa(i)
			if err := store.Claim(name, "password of "+name); err == nil {
				mu.Lock()
				wins = append(wins, name)
				mu.Unlock()
			}
		}()
	}

	close(start)
	wg.Wait()

	if len(wins) != 1 {
		t.Fatalf("%d claims succeeded, want exactly one: %v", len(wins), wins)
	}

	// What is on disk has to be the winner, whole, or the next start either
	// fails to parse it or opens with somebody else's password.
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("the credentials on disk do not parse after a race: %v", err)
	}

	if !reopened.Check(wins[0], "password of "+wins[0]) {
		t.Errorf("the file on disk is not the claim that was told it won (%s)", wins[0])
	}

	// Nothing left behind: a temporary file per write is only tidy if every one
	// of them is renamed away or removed.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, e := range entries {
		if e.Name() != "credentials.json" {
			t.Errorf("left behind in the state directory: %s", e.Name())
		}
	}
}

// A token signed with no key at all is one anybody can make. Before a password
// is chosen there is no key, and without the guard such a token passed.
func TestAnUnclaimedStoreHonoursNoSession(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	payload := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + ":forged"
	forged := payload + "." + sign(nil, payload)

	if store.Valid(forged) {
		t.Error("a session signed with an empty key was accepted before the machine was claimed")
	}

	// Issued by the store itself as well, which signs with the same nothing.
	if store.Valid(store.Issue()) {
		t.Error("an unclaimed store accepted a session it issued with no key")
	}
}

// Parallel guesses from one address. Counting only after the hash let every
// request that arrived during the first one through on a clean record.
func TestAttemptCountsBeforeTheHash(t *testing.T) {
	store := newStore(t)

	const source = "10.0.0.9"

	allowed := 0

	// Nothing is recorded between these calls except by Attempt itself, which
	// is the situation of requests that have all started hashing and none of
	// which has finished.
	for range 50 {
		if ok, _ := store.Attempt(source); ok {
			allowed++
		}
	}

	if allowed > freeAttempts+1 {
		t.Errorf("%d attempts went through without any result being recorded, want at most %d",
			allowed, freeAttempts+1)
	}

	store.Succeeded(source)

	if ok, _ := store.Attempt(source); !ok {
		t.Error("a correct password did not clear what Attempt counted")
	}
}

// Addresses in one /64 are one network choosing its own host bits, and are
// counted as one.
func TestIPv6IsCountedByItsNetwork(t *testing.T) {
	store := newStore(t)

	for i := range freeAttempts + 1 {
		store.Failed("2001:db8:1:2::" + strconv.Itoa(i+1))
	}

	if ok, _ := store.Allow("2001:db8:1:2:dead:beef:0:1"); ok {
		t.Error("a fresh address in the same /64 was given a fresh budget")
	}

	if ok, _ := store.Allow("2001:db8:1:3::1"); !ok {
		t.Error("a different /64 was blocked along with its neighbour")
	}

	// IPv4 stays one address per record.
	for range freeAttempts + 1 {
		store.Failed("192.0.2.1")
	}

	if ok, _ := store.Allow("192.0.2.2"); !ok {
		t.Error("an IPv4 neighbour was blocked along with the address that failed")
	}
}

// The map has a ceiling, and a full one still takes a new address.
func TestTheLimiterForgetsRatherThanGrows(t *testing.T) {
	store := newStore(t)

	for i := range maxSources + 500 {
		store.Failed("10." + strconv.Itoa(i>>16&255) + "." + strconv.Itoa(i>>8&255) + "." + strconv.Itoa(i&255))
	}

	if n := len(store.limiter.by); n > maxSources {
		t.Errorf("the limiter holds %d sources, more than its ceiling of %d", n, maxSources)
	}

	// Expired records are what goes first.
	store.limiter.mu.Lock()
	for _, a := range store.limiter.by {
		a.last = time.Now().Add(-2 * failWindow)
	}
	store.limiter.mu.Unlock()

	store.Failed("192.0.2.99")

	if n := len(store.limiter.by); n != 1 {
		t.Errorf("after everything expired the limiter still holds %d sources, want 1", n)
	}
}

// At most two argon2 hashes at once, whoever asks. Each one is 64 MiB.
func TestHashesAreBounded(t *testing.T) {
	var (
		mu       sync.Mutex
		running  int
		peak     int
		original = idKey
	)

	idKey = func(password, salt []byte, iterations, memory uint32, threads uint8, keyLen uint32) []byte {
		mu.Lock()
		running++
		peak = max(peak, running)
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		running--
		mu.Unlock()

		return make([]byte, keyLen)
	}

	t.Cleanup(func() { idKey = original })

	store := newStore(t)

	var wg sync.WaitGroup

	for range 10 {
		wg.Add(1)

		go func() {
			defer wg.Done()
			store.Check("admin", "anything")
		}()
	}

	wg.Wait()

	if peak > cap(hashSlots) {
		t.Errorf("%d hashes ran at once, want at most %d", peak, cap(hashSlots))
	}

	if peak < 2 {
		t.Errorf("only %d hash ran at a time, so the test measured nothing about the bound", peak)
	}
}
