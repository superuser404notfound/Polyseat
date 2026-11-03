package seat

import (
	"context"
	"os"
	"regexp"
	"strings"
)

// The seat's package mirrors.
//
// The Arch image Incus starts a seat from names exactly one mirror,
// mirrors.kernel.org, which is in Dallas. pacman gives up on a mirror that
// sends less than a byte a second for ten seconds, and with nothing behind it
// the whole transaction goes: on 2026-09-20 that was joser's provisioning and
// on 2026-11-03 louis's, each at the step that pulls a gigabyte or two, where it
// looks like a broken provisioning rather than a slow mirror. Measured from a
// seat: kernel.org 300 KB/s, Arch's own geo mirror 1.3 MB/s.
//
// So a seat whose list is the image's gets a longer one before its first
// package. Not a fixed list of somebody's favourite mirrors, which goes stale:
//   - Arch's own geo mirror first. It is run by Arch, answers from somewhere
//     near whoever asks, and is not going anywhere.
//   - Then the first few of the host's own list, when the host is an Arch or
//     an Arch derivative. That list is already ranked for this network,
//     somebody or reflector having done so, and the seat sits on the same one.
//   - Then whatever the seat had, so the image's mirror stays the last resort.
//
// A list with two servers or more is somebody's choice, or this one, and is
// left exactly as it is.

const seatMirrorlist = "/etc/pacman.d/mirrorlist"

// hostMirrorlist is where an Arch host keeps its list. A variable so that a
// test can point it somewhere.
var hostMirrorlist = "/etc/pacman.d/mirrorlist"

// geoMirror is Arch's own mirror behind GeoDNS.
const geoMirror = "https://geo.mirror.pkgbuild.com/$repo/os/$arch"

// hostMirrors is how many of the host's mirrors are taken.
const hostMirrors = 4

// mirrorLine is a Server line as pacman reads it, and nothing that could be
// more than one: an http or https address with the two variables in it.
var mirrorLine = regexp.MustCompile(`^Server\s*=\s*(https?://[A-Za-z0-9._~:/@%+-]*\$repo[A-Za-z0-9._~:/@%+$-]*)\s*$`)

// mirrorServers are the servers a mirrorlist names, in order.
func mirrorServers(list string) []string {
	var out []string

	for _, line := range strings.Split(list, "\n") {
		if m := mirrorLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, m[1])
		}
	}

	return out
}

// betterMirrorlist is the list a seat should have, given its own and the
// host's, and false when its own is to be kept.
func betterMirrorlist(seat, host string) (string, bool) {
	have := mirrorServers(seat)
	if len(have) >= 2 {
		return "", false
	}

	servers := []string{geoMirror}

	for _, s := range mirrorServers(host) {
		if len(servers) > hostMirrors {
			break
		}

		servers = append(servers, s)
	}

	servers = append(servers, have...)

	seen := map[string]bool{}

	var b strings.Builder

	b.WriteString("# Written by Polyseat, because the image this seat was built from\n")
	b.WriteString("# names one mirror, far away. Arch's own geo mirror, then the first\n")
	b.WriteString("# of the host's, then what the image had. Edit it freely: a list\n")
	b.WriteString("# with two servers or more is left alone.\n")

	for _, s := range servers {
		if seen[s] {
			continue
		}

		seen[s] = true

		b.WriteString("Server = " + s + "\n")
	}

	return b.String(), true
}

// ensureMirrors gives a seat with the image's list a better one, keeping the
// image's beside it as mirrorlist.image. Before any package is fetched.
func (p *Provisioner) ensureMirrors(ctx context.Context) error {
	current, err := p.sh(ctx, "cat "+seatMirrorlist+" 2>/dev/null; exit 0")
	if err != nil {
		return err
	}

	// A host that is not Arch has no such file, and the geo mirror alone is
	// still a long way better than the image.
	host, _ := os.ReadFile(hostMirrorlist)

	list, ok := betterMirrorlist(current, string(host))
	if !ok {
		return nil
	}

	if _, err := p.sh(ctx, "[ -e "+seatMirrorlist+".image ] || cp "+seatMirrorlist+" "+
		seatMirrorlist+".image 2>/dev/null; exit 0"); err != nil {
		return err
	}

	if err := p.Client.PushFile(p.name(), seatMirrorlist, []byte(list), 0o644, 0, 0); err != nil {
		return err
	}

	p.Log("the image's single package mirror was replaced by %d nearer ones",
		len(mirrorServers(list))-len(mirrorServers(current)))

	return nil
}
