// Command polyseatd is the Polyseat daemon.
//
// It owns the seats: it builds them, starts and stops them, keeps their input
// brokers running and serves the web interface that drives all of it. There is
// no command line for any of that on purpose. Seat configuration is generated,
// not written by hand, and a second way in would mean a second author for the
// same files.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/superuser404notfound/Polyseat/internal/api"
	"github.com/superuser404notfound/Polyseat/internal/auth"
	"github.com/superuser404notfound/Polyseat/internal/config"
	"github.com/superuser404notfound/Polyseat/internal/incusx"
	"github.com/superuser404notfound/Polyseat/internal/prepare"
	"github.com/superuser404notfound/Polyseat/internal/report"
	"github.com/superuser404notfound/Polyseat/internal/seat"
	"github.com/superuser404notfound/Polyseat/internal/update"
	"github.com/superuser404notfound/Polyseat/internal/version"
)

func main() {
	configPath := flag.String("config", config.DefaultPath, "path to the bootstrap configuration")
	listen := flag.String("listen", "", "override the listen address from the configuration")
	showVersion := flag.Bool("version", false, "print the version and exit")
	writeReport := flag.Bool("report", false, "describe this installation on stdout and exit, for a bug report")
	showUplink := flag.Bool("uplink", false, "print the interface the seats hang off, and why, then exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("polyseatd", version.Version)

		return
	}

	// Before the root check and before anything is opened, because the report
	// is wanted most on a machine where the daemon will not run. It says which
	// parts it could not read rather than refusing to say anything.
	if *writeReport {
		cfg, err := config.Load(*configPath)
		if err != nil {
			// Not fatal. A configuration that cannot be parsed is one of the
			// things a report should be able to describe, so it carries on with
			// the defaults and says so.
			fmt.Fprintf(os.Stderr, "the configuration could not be read, reporting on the defaults instead: %v\n", err)

			cfg = config.Default()
		}

		report.Write(os.Stdout, cfg, version.Version, time.Now())

		return
	}

	// For host/lan-bridge.sh, which has to work on the same interface this
	// daemon does and used to work it out for itself in shell. It got it wrong
	// on the one machine where the two answers differ, so there is one answer
	// now and this is where it is read from. The name on stdout and the reason
	// on stderr, so a caller can use the first without parsing past the second.
	//
	// Up here with the report, before the root check: what it answers does not
	// depend on being root, and a script that has to run as root should not
	// have to be root twice to ask a question.
	if *showUplink {
		cfg, err := config.Load(*configPath)
		if err != nil {
			cfg = config.Default()
		}

		name, why := seat.Uplink(cfg)

		fmt.Fprintln(os.Stderr, why)

		if name == "" {
			os.Exit(1)
		}

		fmt.Println(name)

		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(*configPath, *listen, logger); err != nil {
		logger.Error("polyseatd stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath, listenOverride string, logger *slog.Logger) error {
	// Root is not optional and saying so plainly beats failing later on a
	// permission denied from the Incus socket. The daemon creates containers,
	// attaches device nodes and runs the broker, none of which an unprivileged
	// process can do.
	if os.Geteuid() != 0 {
		return errors.New("polyseatd has to run as root")
	}

	// Logged at the start of every run because the journal is where the question
	// gets asked. Installing builds a new binary but does not replace a running
	// process, so "which version is actually serving" and "which version is on
	// disk" are two questions, and only this one answers the first.
	logger.Info("polyseatd starting", "version", version.Version)

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	if listenOverride != "" {
		cfg.Listen = listenOverride
	}

	if cfg.Uplink == "" {
		if guess, err := config.DefaultUplink(); err == nil {
			cfg.Uplink = guess
			logger.Info("no uplink configured, using the interface with the default route", "uplink", guess)
		}
	}

	// The password and the certificate come before Incus, because they are
	// needed either way. A machine that cannot reach Incus still serves a page,
	// that page still has to be claimed by whoever gets there first, and it
	// still speaks TLS.
	credentials, err := auth.Open(cfg.StateDir)
	if err != nil {
		return err
	}

	if credentials.NeedsSetup() {
		// Said plainly, because it is the one moment when anybody who reaches
		// the interface can claim it. The alternative was a generated password
		// in this line, which closed that window and cost a terminal to read it
		// back out of the journal.
		logger.Info("no password yet, whoever opens the interface first chooses it")
	}

	certificate, err := auth.EnsureCertificate(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("prepare the TLS certificate: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// On a goroutine of its own rather than on the manager's loop, and nothing
	// waits for it on the way out. It owns no seat, holds nothing anybody is
	// using, and a shutdown that waited for an HTTP request to GitHub to time
	// out would make every restart slower for no gain.
	updates := update.New(version.Version, cfg.UpdateCheck, logger)
	go updates.Run(ctx)

	// Shared by both interfaces below, so that "one run at a time" survives the
	// restart from one into the other.
	preparer := &prepare.Runner{}

	client, err := incusx.Connect()
	if err != nil {
		// Not a reason to exit any more, and this is the whole point of the
		// setup interface. On a machine that has just installed the package
		// this is the ordinary case rather than a fault: Incus is one of the
		// things polyseat-prepare installs, so there is no socket yet. Exiting
		// meant systemd restarted the daemon every five seconds and the
		// interface that exists to explain exactly this never came up.
		return serveSetup(ctx, cfg, certificate,
			api.NewSetup(cfg, credentials, updates, preparer, err, logger),
			preparer, err, logger)
	}

	defer client.Close()

	if incusVersion, err := client.ServerVersion(); err == nil {
		logger.Info("connected to Incus", "version", incusVersion)
	}

	store, err := seat.OpenStore(cfg.StateDir)
	if err != nil {
		return err
	}

	manager := seat.NewManager(cfg, client, store, logger)

	managerDone := make(chan error, 1)
	go func() { managerDone <- manager.Run(ctx) }()

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(manager, credentials, updates, preparer, logger),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}

	serverDone := make(chan error, 1)

	go func() {
		logger.Info("web interface", "address", "https://"+cfg.Listen,
			"certificate", auth.Fingerprint(certificate))

		// TLS always, never plain HTTP. The interface takes a password and is
		// meant to be reachable from the network, and a password sent in clear
		// text would look like protection while providing none.
		err := server.ListenAndServeTLS("", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}

		serverDone <- err
	}()

	// Kept, because it decides the exit status and the exit status decides
	// whether systemd brings this back. The unit says Restart=on-failure, and
	// returning nil after the web interface died would exit 0, which systemd
	// reads as "it finished". The daemon would stay down, systemctl would say
	// "inactive (dead)" rather than "failed", and it would look like somebody
	// had stopped it on purpose.
	var failure error

	// Whether the manager has already been waited for. It is a buffered channel
	// with exactly one value in it, so the wait further down would block for its
	// full fifteen seconds on the one path that had already taken that value,
	// and then warn that the manager had not shut down when it had shut down
	// first.
	managerStopped := false

	select {
	case <-ctx.Done():
		logger.Info("shutting down, the seats keep running")
	case err := <-managerDone:
		managerStopped = true

		if err != nil {
			logger.Error("the seat manager stopped", "error", err)
			failure = err
		}

		stop()
	case err := <-serverDone:
		if err != nil {
			logger.Error("the web interface stopped", "error", err)
			failure = err
		}

		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = server.Shutdown(shutdownCtx)

	if !managerStopped {
		select {
		case <-managerDone:
		case <-time.After(15 * time.Second):
			logger.Warn("the seat manager did not shut down in time")
		}
	}

	return failure
}

// serveSetup runs the interface a machine gets when Incus cannot be reached.
//
// Everything here is the ordinary path with the seats taken out: the same
// address, the same certificate, the same password, and a handler that offers
// preparing the machine instead of managing seats. What it does not do is exit,
// which is what this used to do and what made the problem invisible.
func serveSetup(ctx context.Context, cfg config.Config, certificate tls.Certificate, handler http.Handler, preparer *prepare.Runner, reason error, logger *slog.Logger) error {
	logger.Error("Incus is not answering, so no seat can run on this machine yet",
		"error", reason)
	logger.Info("serving the interface that gets this machine ready instead",
		"address", "https://"+cfg.Listen,
		"prepare", "the page has a button, or run: sudo polyseat-prepare")

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}

	serverDone := make(chan error, 1)

	go func() {
		err := server.ListenAndServeTLS("", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}

		serverDone <- err
	}()

	go watchForIncus(ctx, preparer, logger)

	var failure error

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-serverDone:
		if err != nil {
			logger.Error("the web interface stopped", "error", err)
			failure = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = server.Shutdown(shutdownCtx)

	return failure
}

// incusPoll is how often the setup interface looks to see whether Incus has
// turned up. Not urgent: the two things that make it appear are somebody
// pressing the button on the page, which takes minutes, and a boot where the
// socket was not ready yet, which takes seconds and only happens once.
const incusPoll = 15 * time.Second

// watchForIncus restarts the daemon once there is an Incus to talk to.
//
// The daemon reaches Incus at startup or not at all: the manager, the store and
// every seat hang off that connection, so a daemon that came up without one
// cannot grow the rest of itself afterwards without becoming two daemons in one
// binary. A restart is the honest way across, and it costs nothing here because
// there is nothing running to interrupt.
//
// This is also what makes preparing the machine from the page finish by itself.
// The last thing prepare.sh does is bring Incus up, so within a poll of it
// succeeding the daemon comes back as the real one, and the page it was pressed
// from reloads into the interface it was always meant to be.
func watchForIncus(ctx context.Context, preparer *prepare.Runner, logger *slog.Logger) {
	tick := time.NewTicker(incusPoll)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-tick.C:
			// Not in the middle of preparing the machine. Incus comes up
			// several steps before that script is finished, and a restart here
			// would take the daemon's whole control group with it, pacman
			// included: KillMode=mixed means everything still running gets a
			// SIGKILL. A pacman killed halfway leaves a lock and a partly
			// applied transaction, which is a worse machine than the one this
			// started with.
			if preparer.Running() {
				continue
			}

			client, err := incusx.Connect()
			if err != nil {
				continue
			}

			client.Close()

			// Nothing restarts a daemon nobody started as a service, and
			// telling systemd to restart polyseatd.service from a copy somebody
			// is running by hand would restart the wrong process entirely.
			// systemd sets this for everything it runs.
			if os.Getenv("INVOCATION_ID") == "" {
				logger.Info("Incus is answering now. This polyseatd was not started by systemd, so start it again to pick the seats up")

				return
			}

			logger.Info("Incus is answering now, restarting into the ordinary interface")

			if err := update.Restart(); err != nil {
				// Logged and tried again on the next tick rather than given up
				// on. The page still has its own restart button, and a machine
				// that is ready and says so is not a machine in trouble.
				logger.Error("the restart could not be scheduled", "error", err)

				continue
			}

			return
		}
	}
}
