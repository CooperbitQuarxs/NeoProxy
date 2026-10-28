//go:build !linux || !amd64

package neoscript

import (
	"errors"
	"os/exec"
)

func configureChild(command *exec.Cmd) {}
func confine() error {
	return errors.New("routing scripts require Linux amd64 kernel resource and syscall confinement")
}
