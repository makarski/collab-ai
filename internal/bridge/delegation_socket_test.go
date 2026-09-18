package bridge

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDelegatedWaitRejectsUnrelatedSocketsAndAliases(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "collab-unrelated-")
	requireNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "listen.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	requireNoError(t, err)
	t.Cleanup(func() { ln.Close() })
	alias, err := os.MkdirTemp("/tmp", "collab-listener-")
	requireNoError(t, err)
	t.Cleanup(func() { os.RemoveAll(alias) })
	socketAlias := filepath.Join(alias, "listen.sock")
	requireNoError(t, os.Symlink(path, socketAlias))
	dirAlias := alias + "-link"
	requireNoError(t, os.Symlink(dir, dirAlias))
	t.Cleanup(func() { os.Remove(dirAlias) })
	for _, target := range []string{path, socketAlias, filepath.Join(dirAlias, "listen.sock"), alias + "/../" + filepath.Base(dir) + "/listen.sock"} {
		t.Run(target, func(t *testing.T) {
			_, err := WaitDelegated(context.Background(), target, strings.Repeat("0", 64), delegatedRead{TimeoutSeconds: 1})
			assertErrorContains(t, err, "invalid or unavailable listener delegation")
		})
	}
	// No connection or bearer header may reach the unrelated listener.
	requireNoError(t, ln.SetDeadline(time.Now().Add(30*time.Millisecond)))
	conn, err := ln.Accept()
	if conn != nil {
		conn.Close()
		t.Fatal("delegated wait contacted an unrelated socket")
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("expected untouched listener, got %v", err)
	}
}

func TestDelegationSocketRequiresPrivatePermissionsAndCleansUp(t *testing.T) {
	ln, path, err := listenPrivateSocket()
	requireNoError(t, err)
	dir := filepath.Dir(path)
	t.Cleanup(func() { ln.Close(); os.RemoveAll(dir) })
	requireNoError(t, validateDelegationSocket(path))
	for _, target := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {dir, 0700}} {
		requireNoError(t, os.Chmod(target.path, target.mode|0077))
		assertErrorContains(t, validateDelegationSocket(path), "permissions")
		requireNoError(t, os.Chmod(target.path, target.mode))
	}
	requireNoError(t, ln.Close())
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("listener did not unlink its socket: %v", err)
	}
	requireNoError(t, os.Remove(dir))
}
