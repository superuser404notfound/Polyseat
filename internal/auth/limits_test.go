package auth

import (
	"os"
	"strconv"
	"sync"
	"testing"
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
