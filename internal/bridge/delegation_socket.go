package bridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Only dial the private namespace used by delegate_listener. Lstat rejects
// symlink aliases to unrelated sockets. This retains the existing same-OS-user
// trust boundary; it does not authenticate a hostile process running as us.
func validateDelegationSocket(path string) error {
	dir := filepath.Dir(path)
	if filepath.Clean(path) != path || filepath.Base(path) != "listen.sock" {
		return errors.New("use the unchanged socket_path returned by delegate_listener")
	}
	if filepath.Dir(dir) != "/tmp" || !strings.HasPrefix(filepath.Base(dir), "collab-listener-") {
		return errors.New("socket_path must be in a private /tmp/collab-listener-* directory")
	}
	if err := requirePrivatePath(dir, os.ModeDir|0700); err != nil {
		return err
	}
	return requirePrivatePath(path, os.ModeSocket|0600)
}

func requirePrivatePath(path string, want os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode() != want {
		return fmt.Errorf("delegation path has an unexpected type or permissions: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("delegation path is not owned by this user: %s", path)
	}
	return nil
}
