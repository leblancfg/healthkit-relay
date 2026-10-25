package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// State files in the archive directory: devices.json (stored tokens), codes.json (setup
// codes) and admin.json (the admin token's hash). The server and the subcommands (pair,
// devices, revoke, token create, admin reset) both change them: each change takes an
// exclusive lock on the file's own lock file (.devices.json.lock...), reads the file again if
// another process wrote it, and replaces it atomically. The server rereads a file when it changes on disk, so a
// command takes effect at once.

// stamp identifies the version of a file last read or written.
type stamp struct{ fi os.FileInfo }

// changed tells whether the file at path differs from the one stamped. A missing file
// counts as changed when a file was stamped before.
func (st *stamp) changed(path string) (bool, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return st.fi != nil, nil
	} else if err != nil {
		return false, err
	}
	if st.fi == nil {
		return true, nil
	}
	return !os.SameFile(st.fi, fi) || !fi.ModTime().Equal(st.fi.ModTime()) || fi.Size() != st.fi.Size(), nil
}

// set stamps the file at path as it is now (none, if it is missing).
func (st *stamp) set(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		fi = nil
	}
	st.fi = fi
}

// lockState takes the lock for the state file at path, waiting for another process to
// release it. Setup codes are locked before devices, never the other way round.
func lockState(path string) (unlock func(), err error) {
	dir := filepath.Dir(path)
	f, err := os.OpenFile(filepath.Join(dir, "."+filepath.Base(path)+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	chownLikeDir(f, dir)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil // closing releases the lock
}

// writeFileAtomic replaces path with data (mode 600): a reader sees the old file or the new
// one, whole.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	chownLikeDir(tmp, dir)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// The rename itself must survive a power loss: a revoked token stays revoked.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// chownLikeDir gives a file the owner of dir when running as root, so that a command run
// with sudo leaves files the server can still read and replace.
func chownLikeDir(f *os.File, dir string) {
	if os.Geteuid() != 0 {
		return
	}
	if fi, err := os.Stat(dir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			f.Chown(int(st.Uid), int(st.Gid))
		}
	}
}

// checkStateOwner returns an error when this process may not change the archive directory's
// state: it must run as the directory's owner (the server's user) or as root.
func checkStateOwner(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() == 0 || int(st.Uid) == os.Geteuid() {
		return nil
	}
	return fmt.Errorf("%s belongs to uid %d: run this command as the server's user or as root "+
		"(sudo healthkit-relay <command>, or docker compose exec relay healthkit-relay <command>)", dir, st.Uid)
}
