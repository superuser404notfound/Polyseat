package seat

import (
	"context"
	"time"
)

// protonInterval is how often the daemon looks for a newer build of either
// compatibility tool.
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
var protonIdle = idleProbeFor(cachyOS.dir())

// geIdle is the same question about the other tool. Separate, because a seat
// playing a game under GE should not stop its Proton CachyOS being updated,
// and a game under Proton CachyOS should not stop GE being updated: the two
// directories are replaced independently and only the one being replaced has
// to be quiet.
var geIdle = idleProbeFor(geProton.dir())

// updateProton brings every running seat's Proton CachyOS up to the current
// release.
//
// Cheap when there is nothing to do, which is the overwhelmingly common case:
// one request to GitHub and one file read in the seat say that the current
// build is already there, and nothing is downloaded. It still says so in the
// seat's log, four times a day, because a seat that quietly stopped updating
// would otherwise look exactly like one that had nothing to update.
//
// On a goroutine of its own and at most one at a time. It is called from the
// daemon's main loop, and a pass is a download of several hundred megabytes
// per seat that is behind: run in place, it held up every lifecycle event and
// every sweep for as long as GitHub took to send it.
func (m *Manager) updateProton(ctx context.Context) {
	m.mu.Lock()

	if m.protoning {
		m.mu.Unlock()

		return
	}

	m.protoning = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.protoning = false
			m.mu.Unlock()
		}()

		m.protonPass(ctx)
	}()
}

// protonPass visits the seats one after another.
//
// One at a time, as before, so that four seats behind on the same release are
// four downloads in a row rather than four at once on a machine people are
// playing on. Each seat's turn goes through operate, which is what keeps it
// from running beside a provisioning run or a software update on the same seat:
// both of those replace the same directories, and without the busy flag nothing
// stopped the timer from starting while they did.
func (m *Manager) protonPass(ctx context.Context) {
	seats, err := m.store.List()
	if err != nil {
		return
	}

	for _, s := range seats {
		if err := ctx.Err(); err != nil {
			return
		}

		// Asked before anything is asked of the seat, and asked again by
		// operate, which is the answer that counts. This one only saves a
		// busy seat two execs it would have refused anyway.
		if m.busyWith(s.Name) != "" {
			continue
		}

		status, err := m.client.Status(s.Name)
		if err != nil || status != "Running" {
			continue
		}

		done := make(chan struct{})

		err = m.operate(s.Name, "checking the compatibility tools", func(ctx context.Context) error {
			defer close(done)

			m.protonTurn(ctx, s)

			// Nil whatever happened. Neither step fails for the internet being
			// unreliable, and a seat marked as failed every six hours for
			// something it did not do is worse than the warning below.
			return nil
		})
		if err != nil {
			// Busy after all, taken between the look above and now.
			continue
		}

		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
}

// protonTurn is one seat's share of the pass.
func (m *Manager) protonTurn(ctx context.Context, s Seat) {
	// Streaming is asked first because it is the cheaper question and the
	// commoner reason to stay away. A seat with somebody in it is left
	// alone until the next pass, and the next pass is six hours later,
	// which for a Proton update is no loss at all.
	if m.streaming(ctx, s.Name) {
		m.logf(s.Name, "somebody is streaming, so the compatibility tools wait for the next pass")

		return
	}

	p := &Provisioner{
		Client: m.client,
		Seat:   s,
		Image:  m.cfg.Image,
		Log:    func(f string, a ...any) { m.logf(s.Name, f, a...) },
		uid:    s.PlayerUID,
	}

	// Neither step returns an error for anything that is merely the
	// internet being unreliable, so what comes back here is the seat being
	// unreachable, and the next pass will find that out again.
	if m.nothingUsing(s.Name, protonIdle) {
		if err := p.stepProton(ctx); err != nil {
			m.log.Warn("the Proton update could not run", "seat", s.Name, "err", err)
		}
	}

	// Unless the seat said no. stepGEProton takes the tool away for a seat
	// that has, and that decision belongs to the moment the setting changes
	// rather than to a timer: a seat whose GE was removed here six hours
	// after somebody unticked the box would have spent those hours looking
	// like the setting did nothing.
	//
	// This pass is also how a seat that predates the tool gets it, without
	// waiting to be provisioned for a directory.
	if !s.NoGEProton && m.nothingUsing(s.Name, geIdle) {
		if err := p.stepGEProton(ctx); err != nil {
			m.log.Warn("the GE-Proton update could not run", "seat", s.Name, "err", err)
		}
	}

	// Either of those may have closed an idle Steam to get at a file Steam
	// holds. The seat is meant to have one waiting, so it gets one back
	// here rather than at the next session start, which on a machine that
	// stays on is days away.
	p.ResumeSteam(ctx)
}
