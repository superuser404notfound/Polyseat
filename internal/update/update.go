// Package update tells the daemon whether a newer Polyseat has been published.
//
// It only ever looks. Fetching and installing is host/update.sh, run by
// somebody who picked the moment, because this daemon supervises seats that
// people are playing in: a process that can replace itself halfway through
// somebody's game is not a convenience. What is automated here is the part that
// is safe to automate, which is noticing.
//
// The check is one request to GitHub every six hours and it carries nothing
// about this machine. It can be turned off with "update_check": false in the
// configuration, and then no request is made at all.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// latestAPI is where releases are published. Hard coded rather than derived
// from the git remote of the tree it was built in: the daemon runs from
// /usr/local/bin and has no tree, and an update check that follows whatever
// remote somebody happened to clone from is a way to be pointed at a fork
// without noticing.
const latestAPI = "https://api.github.com/repos/superuser404notfound/Polyseat/releases/latest"

const (
	// interval between checks. A release happens at most weekly and the answer
	// is a banner nobody is waiting for, so asking more often would only spend
	// somebody else's rate limit.
	interval = 6 * time.Hour

	// firstCheck is the delay before the first one, so that a machine switched
	// off overnight still checks in the morning rather than only after six
	// hours of uptime. Not immediate: the daemon has seats to bring up when it
	// starts, and this is the least urgent thing it does.
	firstCheck = time.Minute
)

// client is its own rather than http.DefaultClient, which has no timeout at
// all. A background poll that hangs forever on a half open connection would
// leak one goroutine per check and never say why.
var client = &http.Client{Timeout: 30 * time.Second}

// Release is a published version that this build does not have.
type Release struct {
	// Version is the tag, exactly as published.
	Version string `json:"version"`

	// URL is the release page, which is where the notes are. The interface
	// links to it rather than repeating the notes: what changed is worth
	// reading before an update, and worth reading in full.
	URL string `json:"url"`

	// Published is when it appeared, so that a banner can say "yesterday"
	// rather than only a version number.
	Published time.Time `json:"published"`
}

// Checker holds what the last look at GitHub found.
type Checker struct {
	current string
	enabled bool
	log     *slog.Logger

	// api is where to ask. A field rather than the constant used directly, so
	// that the tests can point it at a server serving a recorded answer from
	// the real one instead of at GitHub.
	api string

	mu     sync.Mutex
	latest *Release
}

// New builds a checker for the version this binary was built from.
func New(current string, enabled bool, log *slog.Logger) *Checker {
	return &Checker{current: current, enabled: enabled, log: log, api: latestAPI}
}

// Available is the newer release, or nil when there is nothing to say. Safe to
// call from the HTTP handlers while the loop is running.
func (c *Checker) Available() *Release {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.latest
}

// Run polls until the context ends. Meant to be run in a goroutine of its own.
func (c *Checker) Run(ctx context.Context) {
	if !c.enabled {
		c.log.Info("not checking for new versions, update_check is off")

		return
	}

	// A build that cannot name itself as a release cannot be compared against
	// one, and saying so once at startup beats a silence that looks like a
	// working check finding nothing. See parseTag for what this refuses and why
	// refusing is the right answer.
	if _, ok := parseTag(c.current); !ok {
		c.log.Info("not checking for new versions, this build is not a released one",
			"version", c.current)

		return
	}

	first := time.NewTimer(firstCheck)
	defer first.Stop()

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-first.C:
			c.check(ctx)

		case <-tick.C:
			c.check(ctx)
		}
	}
}

// check asks once and keeps the answer.
//
// A failure is logged and otherwise dropped. There is no banner for "could not
// reach GitHub": the machine this runs on is a games host on somebody's LAN,
// being offline is a normal state for it, and an interface that complains about
// it teaches people to ignore the place where the real warnings go.
func (c *Checker) check(ctx context.Context) {
	release, err := fetch(ctx, c.api)
	if err != nil {
		c.log.Info("could not check for a new version", "error", err)

		return
	}

	if !newer(c.current, release.Version) {
		c.mu.Lock()
		c.latest = nil
		c.mu.Unlock()

		return
	}

	c.mu.Lock()
	known := c.latest == nil || c.latest.Version != release.Version
	c.latest = release
	c.mu.Unlock()

	// Logged when it changes and not on every check, or a machine left running
	// for a month writes the same line 120 times.
	if known {
		c.log.Info("a newer Polyseat has been published",
			"running", c.current, "available", release.Version, "url", release.URL)
	}
}

// fetch asks GitHub for the current release.
//
// releases/latest and not the tag list: it answers with what was published as a
// release, which excludes drafts and prereleases, and that distinction is the
// whole difference between a version somebody is meant to install and one that
// happens to have a tag.
func fetch(ctx context.Context, api string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	// A repository with no release at all answers 404, and that is an answer
	// rather than a fault. Named separately so the log line does not read like
	// something is broken.
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("nothing has been released yet")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}

	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	// Checked as well as relied upon. releases/latest is documented to leave
	// these out, and a check that costs two comparisons is cheaper than finding
	// out that it does not.
	if body.Draft || body.Prerelease {
		return nil, fmt.Errorf("the latest release is a draft or a prerelease")
	}

	if body.TagName == "" {
		return nil, fmt.Errorf("the latest release has no tag")
	}

	return &Release{
		Version:   body.TagName,
		URL:       body.HTMLURL,
		Published: body.PublishedAt,
	}, nil
}

// version is a tag broken into its three numbers.
type version struct{ major, minor, patch int }

// parseTag reads vMAJOR.MINOR.PATCH and refuses everything else.
//
// Everything else is worth spelling out, because it is most of what the daemon
// can be built as. `git describe` answers with the tag only when the build sits
// exactly on one; a commit after it answers "v0.1.0-4-gabc1234", a tree with
// changes in it gets "-dirty" on the end, a tree without git history gets
// "unknown" and a plain `go build` gets "dev".
//
// None of those can be compared with a release, and the useful thing to do
// about that is nothing at all. A development build is not behind v0.1.0 in any
// sense an installer could fix, and telling somebody to update to something
// older than what they are running is worse than saying nothing.
func parseTag(tag string) (version, bool) {
	fields := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(fields) != 3 {
		return version{}, false
	}

	var out version

	for i, field := range fields {
		// Rejected here rather than left to Atoi: it accepts a leading "+" and
		// a "-", and "0.1.-2" is not a version anybody meant.
		if field == "" || strings.ContainsAny(field, "+-") {
			return version{}, false
		}

		n, err := strconv.Atoi(field)
		if err != nil {
			return version{}, false
		}

		switch i {
		case 0:
			out.major = n
		case 1:
			out.minor = n
		case 2:
			out.patch = n
		}
	}

	return out, true
}

// newer says whether published is a release that running does not have.
//
// False whenever either side is not a plain release tag, so a build from an
// untagged commit is told nothing, and false on equality, so the common case
// stays quiet.
func newer(running, published string) bool {
	have, ok := parseTag(running)
	if !ok {
		return false
	}

	there, ok := parseTag(published)
	if !ok {
		return false
	}

	switch {
	case there.major != have.major:
		return there.major > have.major
	case there.minor != have.minor:
		return there.minor > have.minor
	default:
		return there.patch > have.patch
	}
}
