//go:build unix

package kimi

import "golang.org/x/sys/unix"

// unameReleaseVersion is os.release() and os.version() on Unix.
func unameReleaseVersion() (string, string) {
	var name unix.Utsname
	if unix.Uname(&name) != nil {
		return "", ""
	}
	return unix.ByteSliceToString(name.Release[:]), unix.ByteSliceToString(name.Version[:])
}
