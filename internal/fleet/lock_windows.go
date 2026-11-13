package fleet

import (
	"golang.org/x/sys/windows"
	"os"
)

func Lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ov := &windows.Overlapped{}
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ov); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ov); _ = f.Close() }, nil
}
