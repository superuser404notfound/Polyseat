package seat

import (
	"context"
	"time"
)

// protonInterval is how often the daemon looks for a newer Proton CachyOS.
//
// That build exists because it moves quickly: fixes land in it long before they
// reach a Proton release, and a seat pinned to whatever was current on the day
// it was provisioned would miss the whole point of having it. Six hours rather
// than a minute because the check is a request to GitHub and the answer changes
// perhaps weekly, and rather than a day so that a fix somebody is waiting for
// arrives on the evening it appears rather than the morning after.
const protonInterval = 6 * time.Hour

// protonIdle answers whether the compatibility tool may be replaced right now.
//
// The same question the library asks, about a different directory, and for the
// same reason: the update unlinks a directory and puts another in its place. A
// game running under that Proton keeps the files it already opened, so this is
// not the loud kind of failure, which is exactly what makes it worth avoiding.
// It would open the next one it needs and find it gone, some minutes into
// somebody's evening, with nothing in any log to connect the two.
var protonIdle = idleProbeFor(protonDir + "/" + protonName)

// updateProton brings every running seat's Proton CachyOS up to the current
// release.
//
// Cheap when there is nothing to do, which is the overwhelmingly common case:
// one request to GitHub and one file read in the seat say that the current
// build is already there, and nothing is downloaded. It still says so in the
// seat's log, four times a day, because a seat that quietly stopped updating
// would otherwise look exactly like one that had nothing to update.
func (m *Manager) updateProton(ctx context.Context) {
	seats, err := m.store.List()
	if err != nil {
		return
	}

	for _, s := range seats {
		if err := ctx.Err(); err != nil {
			return
		}

		status, err := m.client.Status(s.Name)
		if err != nil || status != "Running" {
			continue
		}

		// Streaming is asked first because it is the cheaper question and the
		// commoner reason to stay away. A seat with somebody in it is left
		// alone until the next pass, and the next pass is six hours later,
		// which for a Proton update is no loss at all.
		if m.streaming(ctx, s.Name) {
			continue
		}

		if !m.nothingUsing(s.Name, protonIdle) {
			continue
		}

		p := &Provisioner{
			Client: m.client,
			Seat:   s,
			Image:  m.cfg.Image,
			Log:    func(f string, a ...any) { m.logf(s.Name, f, a...) },
			uid:    s.PlayerUID,
		}

		// stepProton never returns an error for anything that is merely the
		// internet being unreliable, so what comes back here is the seat being
		// unreachable, and the next pass will find that out again.
		if err := p.stepProton(ctx); err != nil {
			m.log.Warn("the Proton update could not run", "seat", s.Name, "err", err)
		}
	}
}
