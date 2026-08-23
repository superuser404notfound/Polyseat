// Applying an update from the interface, rather than from a terminal.
//
// The whole of this file exists under one rule: the caller never says what to
// install. It says "install the release the checker found", and everything the
// package is identified by comes from the daemon's own pinned view of GitHub.
// An attacker holding a valid session can therefore make this machine install a
// genuine Polyseat release sooner than its owner meant to, and nothing else.
// That is the property worth keeping when this file is changed.

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// assetHost is where a release asset is allowed to start.
//
// GitHub redirects the download to an object store on a different name, which
// is followed, so this is not a guarantee about where the bytes come from. It
// is a guarantee that the daemon only ever begins a download at a URL on
// GitHub, which is what stops a rewritten API answer from sending it anywhere
// it likes. What the bytes turn out to be is the digest's job.
const assetHost = "github.com"

// assetPathPrefix is the repository this daemon updates itself from, and only
// this one. Pinned for the same reason latestAPI is: a fork's release should
// not be installable here by pointing the check at it.
const assetPathPrefix = "/superuser404notfound/Polyseat/releases/download/"

// maxAsset is the largest package this will download. The real one is about
// seven megabytes; this is room to grow by a lot and still refuse a body that
// intends to fill the disk.
const maxAsset = 256 << 20

// Managed reports whether this installation is one pacman owns.
//
// The question is asked of the running binary rather than of a fixed path,
// because a checkout install lives in /usr/local/bin and a package in /usr/bin,
// and the answer for the one running is the only answer that matters. A
// checkout install is not updated from here: pacman would have nothing to
// replace, and offering a button that cannot work is worse than offering none.
func Managed() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}

	// Resolved, because pacman knows the real path and not a symlink to it.
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}

	// -Qo answers with the owning package or fails. Nothing is written and
	// nothing is downloaded; it reads the local database.
	return exec.Command("pacman", "-Qo", "--", exe).Run() == nil
}

// Apply downloads the release's package, checks it, and installs it.
//
// It does not restart anything. Replacing the binary leaves the running process
// exactly as it was, which is what makes this safe to do while somebody is
// playing, and the restart is a separate decision made at a separate moment.
//
// progress is called with a line per step, for the interface to show. It is
// allowed to be nil.
func Apply(ctx context.Context, rel *Release, progress func(string)) error {
	say := func(format string, args ...any) {
		if progress != nil {
			progress(fmt.Sprintf(format, args...))
		}
	}

	if rel == nil || rel.Package == nil {
		return fmt.Errorf("that release has no package attached to it")
	}

	if !Managed() {
		return fmt.Errorf("this Polyseat was not installed from the package, so pacman has nothing to replace. Update the checkout instead: host/update.sh")
	}

	if err := allowed(rel.Package.URL); err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "polyseat-update-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	// Named from the asset rather than from anything the caller passed, and
	// then only its base, so that a name carrying a path cannot write outside
	// the directory that was just made for it.
	file := filepath.Join(dir, filepath.Base(rel.Package.Name))

	say("downloading %s", rel.Package.Name)

	sum, n, err := download(ctx, rel.Package.URL, file)
	if err != nil {
		return err
	}

	say("downloaded %d bytes", n)

	if err := verify(rel.Package, sum, n); err != nil {
		return err
	}

	say("checksum matches what the release states")
	say("installing with pacman")

	// --noconfirm because there is nobody at a terminal to answer, and no
	// question here has a second sensible answer: the file is the package for
	// the release this daemon already decided to install.
	out, err := exec.CommandContext(ctx, "pacman", "-U", "--noconfirm", "--", file).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pacman refused the package: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	say("installed %s", rel.Version)

	return nil
}

// allowed refuses an asset URL that does not begin where it should.
func allowed(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the release names an address that cannot be read: %w", err)
	}

	if u.Scheme != "https" {
		return fmt.Errorf("the release names a %q address, and only https is followed", u.Scheme)
	}

	if u.Host != assetHost {
		return fmt.Errorf("the release names an asset on %q rather than on %s", u.Host, assetHost)
	}

	if !strings.HasPrefix(u.Path, assetPathPrefix) {
		return fmt.Errorf("the release names an asset outside this project's downloads")
	}

	return nil
}

// download writes the body to path and returns its digest and length.
//
// The digest is taken as the bytes are written rather than by reading the file
// again afterwards. Reading it back would check what is on disk against itself
// if anything could rewrite it in between, which is a check that measures
// nothing.
func download(ctx context.Context, from, path string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, from, nil)
	if err != nil {
		return "", 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("the download answered %s", resp.Status)
	}

	f, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}

	defer func() { _ = f.Close() }()

	sum := sha256.New()

	// One more byte than the limit, so that a body which is exactly too large
	// is caught rather than truncated into looking right.
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, maxAsset+1))
	if err != nil {
		return "", 0, err
	}

	if n > maxAsset {
		return "", 0, fmt.Errorf("the download is larger than %d bytes, which no release of this is", maxAsset)
	}

	return hex.EncodeToString(sum.Sum(nil)), n, f.Sync()
}

// verify compares what arrived against what the release said would arrive.
//
// Both halves come from GitHub, so this says the file is intact and not that it
// is authentic. See the comment on Asset.Digest.
func verify(a *Asset, sum string, n int64) error {
	want, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok || len(want) != sha256.Size*2 {
		return fmt.Errorf("the release states no usable checksum for %s, so it is not installed from here", a.Name)
	}

	if !strings.EqualFold(sum, want) {
		return fmt.Errorf("the download does not match the checksum the release states")
	}

	// Checked after the digest and not instead of it. A length that disagrees
	// with a digest that agrees cannot happen, so this only ever fires
	// alongside the line above; it is here because a release whose stated size
	// is wrong is worth saying out loud rather than passing silently.
	if a.Size != 0 && n != a.Size {
		return fmt.Errorf("the download is %d bytes and the release states %d", n, a.Size)
	}

	return nil
}

// Restart asks systemd to restart the daemon, from outside the daemon.
//
// It cannot be done from inside. `systemctl restart polyseatd` issued by
// polyseatd kills the process that issued it while systemd is still being told
// what to do, and what happens then depends on timing. A transient unit runs
// after this process is gone and is not its child, so the restart survives it.
func Restart() error {
	out, err := exec.Command("systemd-run",
		"--collect",
		"--unit=polyseat-restart",
		"--description=Polyseat: restart the daemon after an update",
		"--on-active=1s",
		"systemctl", "restart", "polyseatd.service",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("the restart could not be scheduled: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	return nil
}
