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

	client, err := incusx.Connect()
	if err != nil {
		return err
	}

	defer client.Close()

	if incusVersion, err := client.ServerVersion(); err == nil {
		logger.Info("connected to Incus", "version", incusVersion)
	}

	store, err := seat.OpenStore(cfg.StateDir)
	if err != nil {
		return err
	}

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

	manager := seat.NewManager(cfg, client, store, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	managerDone := make(chan error, 1)
	go func() { managerDone <- manager.Run(ctx) }()

	// On a goroutine of its own rather than on the manager's loop, and nothing
	// waits for it on the way out. It owns no seat, holds nothing anybody is
	// using, and a shutdown that waited for an HTTP request to GitHub to time
	// out would make every restart slower for no gain.
	updates := update.New(version.Version, cfg.UpdateCheck, logger)
	go updates.Run(ctx)

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(manager, credentials, updates, logger),
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

	select {
	case <-ctx.Done():
		logger.Info("shutting down, the seats keep running")
	case err := <-managerDone:
		if err != nil {
			logger.Error("the seat manager stopped", "error", err)
		}

		stop()
	case err := <-serverDone:
		if err != nil {
			logger.Error("the web interface stopped", "error", err)
		}

		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = server.Shutdown(shutdownCtx)

	select {
	case <-managerDone:
	case <-time.After(15 * time.Second):
		logger.Warn("the seat manager did not shut down in time")
	}

	return nil
}
