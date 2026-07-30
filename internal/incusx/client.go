// Package incusx wraps the Incus client with the handful of operations the
// daemon actually needs.
//
// Everything container runtime specific is meant to live behind this package.
// The rest of the daemon speaks about seats, not about instances, devices or
// operations. That is the same seam the M2 broker prototype drew, for the same
// reason: Incus was chosen for one concrete property, hotplugging device nodes
// into a running container, and if that property ever arrives somewhere else
// the replacement should be one package rather than a rewrite.
//
// The official client is used rather than the REST API by hand. It brings a
// large dependency tree for what is in the end a Unix socket, which is a real
// cost for a daemon running as root, but it also brings the asynchronous
// operation model and the event stream correct on the first try. The trade is
// deliberate and reversible, which is what the seam is for.
package incusx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// ImagesRemote is the default image server Incus ships with.
const ImagesRemote = "https://images.linuxcontainers.org"

// opTimeout bounds the operations that are not a download or somebody's package
// manager: starting, stopping, deleting, changing a device.
//
// Bounded because one of them was not, twice in one hour. Incus reports the
// result of an operation over the connection the client opened, and that
// connection can stop delivering while staying open: the daemon was found parked
// in WaitContext inside Start for a container Incus had already brought up, and
// again inside an exec, while the same commands from a shell answered at once. A
// seat then sits in "starting" with an empty log and no way out but a restart of
// the daemon, which is the worst shape a failure can take.
const opTimeout = 3 * time.Minute

// Client talks to the local Incus daemon.
//
// The connection is replaced rather than only complained about when it stops
// answering. See stalled.
type Client struct {
	mu  sync.RWMutex
	srv incus.InstanceServer

	// dial opens a new connection. A field rather than a call so that the
	// repair below can be tested: the real one needs the Incus socket, which a
	// test running as an ordinary user cannot open, and a repair that quietly
	// fails to happen looks exactly like one that worked.
	dial func() (incus.InstanceServer, error)
}

// bounded gives a context a deadline if it does not have one, leaving a caller
// that set its own alone. Provisioning installs packages for minutes at a time
// and passes its own context; nothing here should shorten that.
func bounded(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}

	return context.WithTimeout(ctx, d)
}

// stalled repairs the connection when a wait timed out, and returns the error.
//
// A timed out wait means Incus accepted the operation and never reported what
// became of it, which in every case seen here was the connection and not the
// operation: the container really had started. So the error is reported once,
// the connection is replaced, and the next call works. The old connection is
// left to be collected rather than disconnected, because the lifecycle listener
// is riding on it and cutting that would take the daemon down with it.
func (c *Client) stalled(err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	c.reconnect()

	return err
}

// reconnect replaces the connection, and says nothing if it cannot.
//
// A failure here leaves the old connection in place, which is no worse than
// before: the caller is already returning an error and the next attempt will try
// again.
func (c *Client) reconnect() {
	if c.dial == nil {
		return
	}

	if srv, err := c.dial(); err == nil {
		c.mu.Lock()
		c.srv = srv
		c.mu.Unlock()
	}
}

// pollInterval is how often an operation is asked about directly while waiting
// for it to announce itself.
const pollInterval = 5 * time.Second

// await waits for an operation, and asks the operation itself as well.
//
// Waiting alone is not enough. The result of an operation reaches the client over
// the event stream, and that stream has been seen to stop delivering while
// staying open. Both halves of building a seat were lost that way on the same
// afternoon: a container created, sitting there stopped with its image fully
// downloaded, and a container started and running, while the daemon waited on
// each for minutes. No deadline fixes that on its own, because it cannot tell a
// stalled wait from an image that is genuinely still downloading.
//
// Refresh is a plain GET against the operation's own URL. It involves no events
// at all, which is exactly why it still answers when the stream does not.
func (c *Client) await(ctx context.Context, op incus.Operation) error {
	done := make(chan error, 1)
	go func() { done <- op.WaitContext(ctx) }()

	poll := time.NewTicker(pollInterval)
	defer poll.Stop()

	for {
		select {
		case err := <-done:
			return c.stalled(err)

		case <-poll.C:
			if op.Refresh() != nil {
				continue
			}

			switch state := op.Get(); state.StatusCode {
			case api.Success:
				// Done, and this was never told, so the connection is the
				// problem rather than the operation. Replaced before anything
				// else uses it.
				c.reconnect()

				return nil

			case api.Failure, api.Cancelled:
				return fmt.Errorf("%s", state.Err)
			}
		}
	}
}

// server is the connection as it stands now.
func (c *Client) server() incus.InstanceServer {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.srv
}

// Connect opens the local Unix socket. The daemon runs as root, so no
// certificate handling is involved.
func Connect() (*Client, error) {
	srv, err := incus.ConnectIncusUnix("", nil)
	if err != nil {
		return nil, fmt.Errorf("connect to Incus: %w", err)
	}

	return &Client{srv: srv, dial: func() (incus.InstanceServer, error) {
		return incus.ConnectIncusUnix("", nil)
	}}, nil
}

// Close releases the connection.
func (c *Client) Close() {
	if c.srv != nil {
		c.server().Disconnect()
	}
}

// ServerVersion reports what the daemon on the other end is, for the doctor.
func (c *Client) ServerVersion() (string, error) {
	info, _, err := c.server().GetServer()
	if err != nil {
		return "", err
	}

	return info.Environment.ServerVersion, nil
}

// ---------------------------------------------------------------- instances

// Status returns the instance status, or an empty string if there is no such
// instance. Callers distinguish "absent" from "stopped" that way without
// having to inspect an error.
func (c *Client) Status(name string) (string, error) {
	state, _, err := c.server().GetInstanceState(name)
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}

		return "", err
	}

	return state.Status, nil
}

// Exists reports whether the instance is present in any state.
func (c *Client) Exists(name string) (bool, error) {
	status, err := c.Status(name)

	return status != "", err
}

// Create makes a container from a remote image and leaves it stopped.
//
// Stopped rather than launched on purpose: provisioning wants to set the
// devices and configuration before anything inside starts, and the caller
// knows better than this package when the container should first come up.
func (c *Client) Create(ctx context.Context, name, image string) error {
	remote, err := incus.ConnectSimpleStreams(ImagesRemote, nil)
	if err != nil {
		return fmt.Errorf("reach the image server: %w", err)
	}

	alias, _, err := remote.GetImageAlias(image)
	if err != nil {
		return fmt.Errorf("look up image %q: %w", image, err)
	}

	img, _, err := remote.GetImage(alias.Target)
	if err != nil {
		return fmt.Errorf("fetch image %q: %w", image, err)
	}

	op, err := c.server().CreateInstanceFromImage(remote, *img, api.InstancesPost{
		Name: name,
		Type: api.InstanceTypeContainer,
	})
	if err != nil {
		return err
	}

	// A remote operation has no context aware Wait, and this one downloads an
	// image, so it is the single call in the daemon that can run for minutes.
	// Cancelling has to be wired up by hand.
	done := make(chan error, 1)
	go func() { done <- op.Wait() }()

	// And so has noticing that it has already finished.
	//
	// The result of an operation reaches the client over the event stream, and
	// that stream has been seen to stop delivering while staying open. A seat was
	// left in "creating the container" for minutes with the container sitting
	// there, made and stopped, and the image fully downloaded: the work was done
	// and only the news of it was lost. There is no deadline that helps, because
	// a real image download legitimately takes minutes.
	//
	// So the operation is asked about directly as well. That is a plain GET
	// against its own URL and involves no events at all, which is exactly why it
	// still works when the stream does not.
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()

	for {
		select {
		case err := <-done:
			return err

		case <-ctx.Done():
			_ = op.CancelTarget()

			return ctx.Err()

		case <-poll.C:
			target, err := op.GetTarget()
			if err != nil || target == nil {
				continue
			}

			live, _, err := c.server().GetOperation(target.ID)
			if err != nil {
				continue
			}

			switch live.StatusCode {
			case api.Success:
				// Finished, and this was never told. The operation is not the
				// problem, the connection is, so it is replaced before anything
				// else uses it.
				c.reconnect()

				return nil

			case api.Failure, api.Cancelled:
				return fmt.Errorf("create %s: %s", name, live.Err)
			}
		}
	}
}

// Delete removes a stopped instance.
func (c *Client) Delete(ctx context.Context, name string) error {
	ctx, cancel := bounded(ctx, opTimeout)
	defer cancel()

	op, err := c.server().DeleteInstance(name)
	if err != nil {
		return err
	}

	return c.await(ctx, op)
}

func (c *Client) changeState(ctx context.Context, name, action string, timeout int, force bool) error {
	ctx, cancel := bounded(ctx, opTimeout)
	defer cancel()

	op, err := c.server().UpdateInstanceState(name, api.InstanceStatePut{
		Action:  action,
		Timeout: timeout,
		Force:   force,
	}, "")
	if err != nil {
		return err
	}

	return c.await(ctx, op)
}

// Start boots the instance.
func (c *Client) Start(ctx context.Context, name string) error {
	return c.changeState(ctx, name, "start", -1, false)
}

// Stop asks the instance to shut down, giving it timeout seconds before it is
// killed. Callers are expected to have stopped anything that talks to the
// instance first, see the note on Exec.
func (c *Client) Stop(ctx context.Context, name string, timeout int) error {
	return c.changeState(ctx, name, "stop", timeout, false)
}

// Kill stops the instance without waiting for anything inside it.
func (c *Client) Kill(ctx context.Context, name string) error {
	return c.changeState(ctx, name, "stop", 0, true)
}

// Restart stops and starts in one operation.
func (c *Client) Restart(ctx context.Context, name string, timeout int) error {
	return c.changeState(ctx, name, "restart", timeout, false)
}

// ------------------------------------------------------------------ config

// Instance returns the current configuration together with its ETag, which
// every update has to carry back so concurrent writers cannot overwrite each
// other silently.
func (c *Client) Instance(name string) (*api.Instance, string, error) {
	return c.server().GetInstance(name)
}

// idmapEntry is one line of an instance's identifier mapping, as Incus records
// it in volatile.idmap.current.
type idmapEntry struct {
	Isuid    bool  `json:"Isuid"`
	Isgid    bool  `json:"Isgid"`
	Hostid   int64 `json:"Hostid"`
	Nsid     int64 `json:"Nsid"`
	Maprange int64 `json:"Maprange"`
}

// MapID translates a uid and gid inside a container to what they are on the
// host.
//
// Needed wherever the host writes files a container has to own. A directory
// bind mounted into an unprivileged container is stored with host identifiers,
// so a file created as uid 1000 on the host is nobody's inside; the player's
// uid 1000 in the container is 1001000 outside it.
//
// Read from the instance rather than assumed. Incus gives every container the
// same range by default, which is why cloning between seats works at all, but
// security.idmap.isolated changes that per container and a daemon that had
// hardcoded the common case would write unreadable files without saying so.
func (c *Client) MapID(name string, uid, gid int64) (hostUID, hostGID int64, err error) {
	inst, _, err := c.server().GetInstance(name)
	if err != nil {
		return 0, 0, err
	}

	raw := inst.Config["volatile.idmap.current"]
	if raw == "" {
		// A privileged container has no mapping, so the identifiers pass
		// straight through.
		return uid, gid, nil
	}

	var entries []idmapEntry

	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return 0, 0, fmt.Errorf("%s: volatile.idmap.current: %w", name, err)
	}

	hostUID, hostGID = int64(-1), int64(-1)

	for _, e := range entries {
		if e.Isuid && uid >= e.Nsid && uid < e.Nsid+e.Maprange {
			hostUID = e.Hostid + (uid - e.Nsid)
		}

		if e.Isgid && gid >= e.Nsid && gid < e.Nsid+e.Maprange {
			hostGID = e.Hostid + (gid - e.Nsid)
		}
	}

	if hostUID < 0 || hostGID < 0 {
		return 0, 0, fmt.Errorf("%s: uid %d and gid %d are outside the container's mapped range", name, uid, gid)
	}

	return hostUID, hostGID, nil
}

// Configure merges configuration keys and devices into the instance and reports
// whether anything actually changed. A device mapped to nil is removed. Absent
// keys are left untouched, so this converges rather than replaces.
func (c *Client) Configure(ctx context.Context, name string, config map[string]string, devices map[string]map[string]string) (bool, error) {
	ctx, cancel := bounded(ctx, opTimeout)
	defer cancel()

	inst, etag, err := c.server().GetInstance(name)
	if err != nil {
		return false, err
	}

	put := inst.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}

	if put.Devices == nil {
		put.Devices = map[string]map[string]string{}
	}

	changed := false

	for k, v := range config {
		if put.Config[k] != v {
			put.Config[k] = v
			changed = true
		}
	}

	for name, dev := range devices {
		if dev == nil {
			if _, ok := put.Devices[name]; ok {
				delete(put.Devices, name)
				changed = true
			}

			continue
		}

		if !sameDevice(put.Devices[name], dev) {
			put.Devices[name] = dev
			changed = true
		}
	}

	// Nothing to do is the common case once a seat is provisioned. Reporting
	// that back matters: it is what lets provisioning skip the container
	// restart that enabling the driver injection needs, so re-provisioning a
	// healthy seat does not interrupt it for no reason.
	if !changed {
		return false, nil
	}

	op, err := c.server().UpdateInstance(name, put, etag)
	if err != nil {
		return false, err
	}

	return true, c.await(ctx, op)
}

// UnsetConfig removes configuration keys. Separate from Configure because a
// merge cannot express "absent" without giving up the ability to set an empty
// value.
func (c *Client) UnsetConfig(ctx context.Context, name string, keys ...string) error {
	ctx, cancel := bounded(ctx, opTimeout)
	defer cancel()

	inst, etag, err := c.server().GetInstance(name)
	if err != nil {
		return err
	}

	put := inst.Writable()

	changed := false

	for _, k := range keys {
		if _, ok := put.Config[k]; ok {
			delete(put.Config, k)
			changed = true
		}
	}

	if !changed {
		return nil
	}

	op, err := c.server().UpdateInstance(name, put, etag)
	if err != nil {
		return err
	}

	return c.await(ctx, op)
}

func sameDevice(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

// Addresses reports the IPv4 addresses per interface of a running instance.
// Used to generate Sunshine's allowed CSRF origins, which is why it is the
// running address that matters rather than anything configured.
func (c *Client) Addresses(name string) (map[string][]string, error) {
	state, _, err := c.server().GetInstanceState(name)
	if err != nil {
		return nil, err
	}

	out := map[string][]string{}

	for iface, net := range state.Network {
		if iface == "lo" {
			continue
		}

		for _, addr := range net.Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				out[iface] = append(out[iface], addr.Address)
			}
		}
	}

	return out, nil
}

// -------------------------------------------------------------------- exec

// ErrExec reports a command that ran but failed.
type ErrExec struct {
	Argv     []string
	ExitCode int
	Output   string
}

// Error reports the command, its exit code and the END of its output.
//
// The end, not the beginning. The first version kept the first 400 characters,
// and a failing pacman spends those on warnings about packages that are already
// up to date while the actual reason sits in the last line. That turned a clear
// file conflict into a message that said nothing.
func (e *ErrExec) Error() string {
	out := strings.TrimSpace(e.Output)
	if len(out) > 600 {
		out = "... " + out[len(out)-600:]
	}

	return fmt.Sprintf("%s: exit %d: %s", strings.Join(e.Argv, " "), e.ExitCode, out)
}

// Exec runs a command inside the instance as root and returns its exit code.
//
// One warning that cost a wedged Incus daemon once: do not run this against an
// instance that is shutting down. The M2 broker prototype polled with `incus
// exec` on a half second interval, an exec landed in the middle of a stop, and
// the whole daemon hung in "Stopping instance" while the container was already
// gone. Whoever calls this is responsible for knowing the instance is running,
// which is why the daemon tracks lifecycle events rather than polling.
func (c *Client) Exec(ctx context.Context, name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	req := api.InstanceExecPost{
		Command:     argv,
		WaitForWS:   true,
		Interactive: false,
		Environment: map[string]string{
			"HOME":            "/root",
			"PATH":            "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"DEBIAN_FRONTEND": "noninteractive",
		},
	}

	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}

	if stdout == nil {
		stdout = io.Discard
	}

	if stderr == nil {
		stderr = io.Discard
	}

	done := make(chan bool)

	op, err := c.server().ExecInstance(name, req, &incus.InstanceExecArgs{
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		DataDone: done,
	})
	if err != nil {
		return -1, err
	}

	if err := c.await(ctx, op); err != nil {
		return -1, err
	}

	// The operation finishes before the streams have drained. Without this
	// wait the last lines of output are lost, which turns a useful error
	// message into an empty one.
	select {
	case <-done:
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-time.After(30 * time.Second):
	}

	code := -1
	if raw, ok := op.Get().Metadata["return"]; ok {
		if f, ok := raw.(float64); ok {
			code = int(f)
		}
	}

	return code, nil
}

// Run executes a command and returns its combined output, treating a non-zero
// exit as an error. This is the form nearly all provisioning uses.
func (c *Client) Run(ctx context.Context, name string, argv ...string) (string, error) {
	return c.RunInput(ctx, name, "", argv...)
}

// RunInput is Run with something on standard input.
func (c *Client) RunInput(ctx context.Context, name, stdin string, argv ...string) (string, error) {
	out := &syncBuffer{}

	var in io.Reader
	if stdin != "" {
		in = strings.NewReader(stdin)
	}

	code, err := c.Exec(ctx, name, argv, in, out, out)
	if err != nil {
		return out.String(), err
	}

	if code != 0 {
		return out.String(), &ErrExec{Argv: argv, ExitCode: code, Output: out.String()}
	}

	return out.String(), nil
}

// Try is Run for commands whose failure is information rather than a problem.
// It returns the output and the exit code and only errors when the command
// could not be run at all.
func (c *Client) Try(ctx context.Context, name string, argv ...string) (string, int, error) {
	out := &syncBuffer{}

	code, err := c.Exec(ctx, name, argv, nil, out, out)

	return out.String(), code, err
}

// syncBuffer collects standard output and standard error together.
//
// It has to be locked. The client copies the two streams from two goroutines,
// and handing both the same bytes.Buffer loses output: this first showed up as
// `id -u player` returning nothing at all, which read like a missing user
// rather than a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// -------------------------------------------------------------------- files

// PushFile writes a file inside the instance, creating parent directories.
func (c *Client) PushFile(name, path string, content []byte, mode int, uid, gid int64) error {
	if dir := parentDir(path); dir != "" {
		if err := c.MakeDir(name, dir, 0o755, uid, gid); err != nil {
			return err
		}
	}

	return c.server().CreateInstanceFile(name, path, incus.InstanceFileArgs{
		Content:   bytes.NewReader(content),
		UID:       uid,
		GID:       gid,
		Mode:      mode,
		Type:      "file",
		WriteMode: "overwrite",
	})
}

// MakeDir creates a directory inside the instance, and every parent it needs.
//
// The Incus file API creates exactly one level, so pushing a file into a path
// like /usr/share/glvnd/egl_vendor.d fails with a bare "Not Found" that says
// nothing about which component was missing. Existing directories are left
// alone rather than reported, so provisioning stays idempotent.
func (c *Client) MakeDir(name, path string, mode int, uid, gid int64) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")

	for i := range parts {
		current := "/" + strings.Join(parts[:i+1], "/")

		err := c.server().CreateInstanceFile(name, current, incus.InstanceFileArgs{
			UID:  uid,
			GID:  gid,
			Mode: mode,
			Type: "directory",
		})
		// Only the last component's failure is worth reporting. If it was
		// created, every parent was there; and asking Incus to create a
		// directory that already exists is not reliably one recognisable
		// error across versions.
		if err != nil && i == len(parts)-1 &&
			!strings.Contains(strings.ToLower(err.Error()), "exists") {
			return fmt.Errorf("create %s: %w", current, err)
		}
	}

	return nil
}

// ReadFile returns the contents of a file inside the instance.
func (c *Client) ReadFile(name, path string) ([]byte, error) {
	reader, _, err := c.server().GetInstanceFile(name, path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = reader.Close() }()

	return io.ReadAll(reader)
}

func parentDir(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return ""
	}

	return path[:i]
}

// ------------------------------------------------------------------- events

// Lifecycle is one instance lifecycle event, reduced to what the daemon cares
// about.
type Lifecycle struct {
	Action   string // instance-started, instance-shutdown, instance-deleted, ...
	Instance string
	Time     time.Time
}

// Lifecycles streams instance lifecycle events until the context ends.
//
// This exists so the daemon never has to poll Incus to learn what state a
// container is in. Polling is what wedged the daemon during the M2 spike, and
// beyond that a seat can be stopped from outside the daemon entirely, by a
// crash or by somebody typing `incus stop`. The daemon has to notice either
// way.
func (c *Client) Lifecycles(ctx context.Context) (<-chan Lifecycle, error) {
	listener, err := c.server().GetEventsByType([]string{"lifecycle"})
	if err != nil {
		return nil, err
	}

	out := make(chan Lifecycle, 32)

	_, err = listener.AddHandler(nil, func(ev api.Event) {
		var meta api.EventLifecycle
		if json.Unmarshal(ev.Metadata, &meta) != nil {
			return
		}

		if !strings.HasPrefix(meta.Action, "instance-") {
			return
		}

		// The source is a URL like /1.0/instances/seat1.
		name := meta.Source
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}

		if name == "" {
			return
		}

		select {
		case out <- Lifecycle{Action: meta.Action, Instance: name, Time: ev.Timestamp}:
		default:
			// A full channel means the consumer is stuck. Dropping is
			// correct here: the daemon reconciles against the real
			// status anyway, so a lost event costs a delay, never
			// correctness.
		}
	})
	if err != nil {
		listener.Disconnect()

		return nil, err
	}

	go func() {
		defer close(out)
		defer listener.Disconnect()

		wait := make(chan error, 1)
		go func() { wait <- listener.Wait() }()

		select {
		case <-ctx.Done():
		case <-wait:
		}
	}()

	return out, nil
}

// ------------------------------------------------------------------ helpers

func isNotFound(err error) bool {
	return err != nil && (api.StatusErrorCheck(err, 404) ||
		strings.Contains(strings.ToLower(err.Error()), "not found"))
}

// ErrNotFound is returned for an instance that does not exist.
var ErrNotFound = errors.New("instance not found")
