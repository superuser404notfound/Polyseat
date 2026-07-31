package seat

import (
	"os"
	"path/filepath"
)

// isBridge reports whether the named host interface is a Linux bridge.
//
// This one fact decides how a seat reaches the LAN, and it is read off the
// interface rather than configured, because it is not a preference: an
// interface either is a bridge or it is not, and attaching the wrong kind of
// NIC to it fails or, worse, half works.
//
// The kernel gives every bridge a bridge/ directory under its sysfs entry and
// gives it to nothing else. Asking that is exact and needs no tools; parsing
// the output of ip link would be the same answer with a fork and a format that
// is allowed to change.
func isBridge(iface string) bool {
	if iface == "" {
		return false
	}

	// Cleaned and checked, because the name comes out of the configuration file
	// and is about to be joined onto a path. A value like ../../ would otherwise
	// ask about a directory somewhere else entirely.
	if iface != filepath.Base(iface) || iface == "." || iface == ".." {
		return false
	}

	info, err := os.Stat(filepath.Join("/sys/class/net", iface, "bridge"))

	return err == nil && info.IsDir()
}
