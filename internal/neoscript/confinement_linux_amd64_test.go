//go:build linux && amd64

package neoscript

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

// This test-only mode probes actual denied kernel operations, not mocks of Lua's
// globals. Probe failure is visible as subprocess exit, not a server crash.
func confinementProbe(operation string) int {
	parentPID := os.Getppid()
	if err := confine(); err != nil {
		return 10
	}
	var err error
	switch operation {
	case "filesystem-read":
		var fd int
		fd, err = unix.Open("/etc/passwd", unix.O_RDONLY, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
	case "filesystem-write":
		var fd int
		fd, err = unix.Open("/neoproxy-script-must-not-create", unix.O_CREAT|unix.O_WRONLY, 0600)
		if err == nil {
			_ = unix.Close(fd)
		}
	case "network":
		var fd int
		fd, err = unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
	case "exec":
		err = unix.Exec("/bin/true", []string{"true"}, []string{})
	case "signal-host":
		err = unix.Kill(parentPID, 0)
	case "ptrace":
		err = unix.PtraceAttach(parentPID)
	case "read-stderr":
		var buffer [1]byte
		_, err = unix.Read(2, buffer[:])
	default:
		return 11
	}
	if !errors.Is(err, unix.EPERM) {
		return 12
	}
	return 0
}

func TestKernelConfinementDeniesHostCapabilities(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"filesystem-read", "filesystem-write", "network", "exec", "signal-host", "ptrace", "read-stderr"} {
		t.Run(operation, func(t *testing.T) {
			command := exec.Command(executable, "--test-script-confinement", operation)
			command.Env = []string{"GOMAXPROCS=1", "GOMEMLIMIT=64MiB", "GOTRACEBACK=none"}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("kernel probe %s: %v %s", operation, err, output)
			}
		})
	}
}
