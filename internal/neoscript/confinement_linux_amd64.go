//go:build linux && amd64

package neoscript

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func configureChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func confine() error {
	// no_new_privs is thread-local until seccomp synchronizes the filter. Do not
	// let the Go scheduler migrate between setting it and installing TSYNC.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, limit := range []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_DATA, 192 << 20},
		{unix.RLIMIT_AS, 2 << 30},
		{unix.RLIMIT_CPU, 1},
		{unix.RLIMIT_CORE, 0},
		{unix.RLIMIT_FSIZE, 0},
	} {
		if err := unix.Setrlimit(limit.resource, &unix.Rlimit{Cur: limit.value, Max: limit.value}); err != nil {
			return fmt.Errorf("set resource limit: %w", err)
		}
	}
	// DATA limits are not a reliable heap ceiling on every Linux configuration.
	// AS is the hard outer bound, including Go's reserved address ranges. Setting
	// it below an already inherited mapping does not shrink that mapping, so
	// reject such a worker rather than claim a ceiling it already exceeds.
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return fmt.Errorf("inspect worker address space: %w", err)
	}
	fields := strings.Fields(string(statm))
	if len(fields) == 0 {
		return fmt.Errorf("inspect worker address space: empty statm")
	}
	pages, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || pages > (2<<30)/uint64(os.Getpagesize()) {
		return fmt.Errorf("worker address space already exceeds 2GiB ceiling")
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("disable dumps: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no-new-privileges: %w", err)
	}
	filter := workerFilter(uint32(os.Getpid()))
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	result, _, errno := unix.Syscall6(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("install synchronized syscall filter: %w", errno)
	}
	if result != 0 {
		return fmt.Errorf("synchronize syscall filter with thread %d", result)
	}
	return nil
}

// This allowlist is deliberately runtime-only. There is no open, socket, exec,
// fork, ptrace, io_uring, keyring or mount syscall. Child pipes are the only I/O.
// Offsets are the Linux seccomp_data ABI: nr=0, arch=4, args[0]=16.
func workerFilter(pid uint32) []unix.SockFilter {
	stmt := func(code uint16, k uint32) unix.SockFilter { return unix.SockFilter{Code: code, K: k} }
	jump := func(k uint32, yes, no uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: k, Jt: yes, Jf: no}
	}
	load := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	ret := uint16(unix.BPF_RET | unix.BPF_K)
	deny := uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)
	filter := []unix.SockFilter{stmt(load, 4), jump(unix.AUDIT_ARCH_X86_64, 1, 0), stmt(ret, unix.SECCOMP_RET_KILL_PROCESS), stmt(load, 0)}
	for _, number := range []uint32{
		unix.SYS_CLOSE, unix.SYS_FSTAT, unix.SYS_LSEEK,
		unix.SYS_MMAP, unix.SYS_MPROTECT, unix.SYS_MUNMAP, unix.SYS_MREMAP, unix.SYS_MADVISE, unix.SYS_BRK,
		unix.SYS_FUTEX, unix.SYS_SCHED_YIELD, unix.SYS_SCHED_GETAFFINITY,
		unix.SYS_RT_SIGACTION, unix.SYS_RT_SIGPROCMASK, unix.SYS_RT_SIGRETURN, unix.SYS_SIGALTSTACK,
		unix.SYS_GETPID, unix.SYS_GETTID, unix.SYS_GETRANDOM,
		unix.SYS_NANOSLEEP, unix.SYS_CLOCK_NANOSLEEP, unix.SYS_CLOCK_GETTIME, unix.SYS_GETTIMEOFDAY,
		unix.SYS_SET_ROBUST_LIST, unix.SYS_RSEQ,
		unix.SYS_EPOLL_CREATE1, unix.SYS_EPOLL_CTL, unix.SYS_EPOLL_PWAIT, unix.SYS_EPOLL_WAIT, unix.SYS_EVENTFD2,
		unix.SYS_EXIT, unix.SYS_EXIT_GROUP,
	} {
		filter = append(filter, jump(number, 0, 1), stmt(ret, unix.SECCOMP_RET_ALLOW))
	}
	// read(stdin) and write(stdout/stderr) only. Reject alternative descriptor I/O
	// even if a future embedding accidentally inherits an extra descriptor.
	filter = append(filter, jump(unix.SYS_READ, 0, 4), stmt(load, 16), jump(0, 0, 1), stmt(ret, unix.SECCOMP_RET_ALLOW), stmt(ret, deny), stmt(load, 0))
	for _, number := range []uint32{unix.SYS_WRITE, unix.SYS_WRITEV} {
		filter = append(filter, jump(number, 0, 5), stmt(load, 16), jump(1, 1, 0), jump(2, 0, 1), stmt(ret, unix.SECCOMP_RET_ALLOW), stmt(ret, deny), stmt(load, 0))
	}
	// Runtime signals can target only this worker's thread group.
	filter = append(filter, jump(unix.SYS_TGKILL, 0, 4), stmt(load, 16), jump(pid, 0, 1), stmt(ret, unix.SECCOMP_RET_ALLOW), stmt(ret, deny), stmt(load, 0))
	// A runtime thread needs CLONE_THREAD|CLONE_VM|CLONE_SIGHAND. Independent
	// processes cannot use CLONE_THREAD; clone3 returns ENOSYS for safe fallback.
	required := uint32(unix.CLONE_THREAD | unix.CLONE_VM | unix.CLONE_SIGHAND)
	filter = append(filter, jump(unix.SYS_CLONE, 0, 5), stmt(load, 16), stmt(unix.BPF_ALU|unix.BPF_AND|unix.BPF_K, required), jump(required, 0, 1), stmt(ret, unix.SECCOMP_RET_ALLOW), stmt(ret, deny), stmt(load, 0))
	filter = append(filter, jump(unix.SYS_CLONE3, 0, 1), stmt(ret, uint32(unix.SECCOMP_RET_ERRNO)|uint32(unix.ENOSYS)), stmt(ret, deny))
	return filter
}
