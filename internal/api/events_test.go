package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Shutdown waits for every request, and an event stream never ends on its own:
// every open page held each restart of the daemon for the whole ten seconds.
// The handler here waits the way events does, on stopping and on its context.
func TestAStreamEndsWhenTheServerStops(t *testing.T) {
	entered := make(chan struct{})

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)

		select {
		case <-r.Context().Done():
		case <-stopping(r):
		}
	}))

	EndStreamsOnShutdown(server.Config)
	server.Start()

	defer server.Close()

	// Run first: without the fix the stream is still open when the test ends,
	// and server.Close would wait for it for ever rather than let the failure
	// be reported.
	defer server.Config.Close()

	// Read to the end, the way EventSource does. Closing the body early would
	// close the connection, and the stream would end for that reason instead,
	// which is how the first version of this test passed with the fix removed.
	go func() {
		resp, err := http.Get(server.URL)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()

	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()

	if err := server.Config.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown waited %v for an open stream and gave up: %v", time.Since(start), err)
	}
}

// A request that is not a stream is still waited for: ending those is what the
// wait in Shutdown is there to prevent.
func TestOtherRequestsAreStillWaitedFor(t *testing.T) {
	entered := make(chan struct{})
	finished := make(chan bool, 1)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		time.Sleep(300 * time.Millisecond)
		finished <- r.Context().Err() == nil
	}))

	EndStreamsOnShutdown(server.Config)
	server.Start()

	defer server.Close()

	go func() {
		resp, err := http.Get(server.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := server.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case live := <-finished:
		if !live {
			t.Error("an ordinary request had its context cancelled by the shutdown")
		}
	default:
		t.Error("Shutdown returned before an ordinary request had finished")
	}
}
