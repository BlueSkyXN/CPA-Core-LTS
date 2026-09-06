package flowcontrol

import "syscall"

func filesystemFree() *uint64 {
	var s syscall.Statfs_t
	if syscall.Statfs(".", &s) != nil {
		return nil
	}
	n := uint64(s.Bavail) * uint64(s.Bsize)
	return &n
}
