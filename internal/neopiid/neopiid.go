// Package neopiid owns the NeoPi installation identity that NeoPi-ported
// providers send, one per NeoProxy auth directory as NeoPi keeps one per install.
package neopiid

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// fileName predates the shared identity; renaming it would change the
// identity already sent by existing deployments.
const fileName = ".claude-neopi-install-id"

var mu sync.Mutex

// InstallID returns the persistent installation identity beneath authDir,
// creating it on first use. Only this non-secret sidecar is written; a fully
// written inode is linked atomically so concurrent processes cannot observe an
// empty or partially written identity.
func InstallID(authDir string) (string, error) {
	if strings.TrimSpace(authDir) == "" {
		return "", fmt.Errorf("NeoPi installation identity requires AuthDir")
	}
	mu.Lock()
	defer mu.Unlock()
	path := filepath.Join(authDir, fileName)
	read := func() (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(data))
		if _, err = uuid.Parse(id); err != nil {
			return "", fmt.Errorf("invalid NeoPi installation identity: %w", err)
		}
		return id, nil
	}
	if id, err := read(); err == nil {
		return id, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(authDir, ".neopi-install-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(uuid.NewString()); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = os.Link(file.Name(), path); err != nil && !os.IsExist(err) {
		return "", err
	}
	return read()
}
