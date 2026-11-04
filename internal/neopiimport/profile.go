// Package neopiimport reads credentials out of a local NeoPi profile and maps
// them onto NeoProxy's engine formats. It never writes to the NeoPi profile:
// the database is copied to a private temporary directory and read there.
package neopiimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DBFileName is NeoPi's credential database inside a profile directory.
const DBFileName = "agent.db"

var (
	// ErrNotProfile reports a path that is not a NeoPi profile directory.
	ErrNotProfile = errors.New("not a NeoPi profile: expected a directory containing agent.db with NeoPi's credential table")
	// ErrCredentialNotFound reports a credential id that no longer exists in the profile.
	ErrCredentialNotFound = errors.New("this credential no longer exists in the NeoPi profile; it may have been removed or replaced in NeoPi")
)

// Credential is one row of NeoPi's auth_credentials table. The secret payload
// is unexported so it cannot be serialized or logged by accident.
type Credential struct {
	ID       string
	Provider string
	// Type is NeoPi's credential_type: "api_key" or "oauth".
	Type     string
	Disabled bool
	// UpdatedAt is epoch milliseconds of NeoPi's last write to the row.
	UpdatedAt int64
	data      map[string]any
	// baseURL is the provider address NeoPi stores beside the credential in the
	// profile (Marineris), or empty.
	baseURL string
}

// DefaultProfile returns the profile NeoPi itself uses for the user running
// NeoProxy: PI_CODING_AGENT_DIR when set, otherwise ~/.omp/agent.
func DefaultProfile() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return expandHome(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".omp", "agent"), nil
}

// ResolveProfile validates a profile directory (empty means DefaultProfile)
// and returns its cleaned absolute path.
func ResolveProfile(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return DefaultProfile()
	}
	dir, err := expandHome(dir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w (the profile path must be absolute)", ErrNotProfile)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", ErrNotProfile
	}
	db, err := os.Stat(filepath.Join(dir, DBFileName))
	if err != nil || !db.Mode().IsRegular() {
		return "", ErrNotProfile
	}
	return dir, nil
}

func expandHome(dir string) (string, error) {
	if dir != "~" && !strings.HasPrefix(dir, "~/") {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(dir, "~")), nil
}

// Read returns every credential row of a validated profile, oldest first.
func Read(ctx context.Context, profile string) ([]Credential, error) {
	snapshot, cleanup, err := snapshotDB(profile)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	db, err := sql.Open("sqlite3", "file:"+snapshot+"?_busy_timeout=2000")
	if err != nil {
		return nil, fmt.Errorf("open NeoPi credential snapshot: %w", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, provider, credential_type, data, disabled_cause IS NOT NULL, updated_at FROM auth_credentials ORDER BY id`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "not a database") || strings.Contains(err.Error(), "no such column") {
			return nil, ErrNotProfile
		}
		return nil, fmt.Errorf("read NeoPi credentials: %w", err)
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var (
			id        int64
			c         Credential
			raw       string
			updatedAt sql.NullInt64
		)
		if err = rows.Scan(&id, &c.Provider, &c.Type, &raw, &c.Disabled, &updatedAt); err != nil {
			return nil, fmt.Errorf("read NeoPi credentials: %w", err)
		}
		c.ID = strconv.FormatInt(id, 10)
		c.baseURL = profileBaseURL(profile, c.Provider)
		if updatedAt.Valid {
			c.UpdatedAt = updatedAt.Int64 * 1000
		}
		if json.Unmarshal([]byte(raw), &c.data) != nil || c.data == nil {
			c.data = map[string]any{}
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read NeoPi credentials: %w", err)
	}
	return out, nil
}

// Find returns one credential by NeoPi row id.
func Find(ctx context.Context, profile, id string) (Credential, error) {
	all, err := Read(ctx, profile)
	if err != nil {
		return Credential{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Credential{}, ErrCredentialNotFound
}

// snapshotDB copies agent.db and its WAL into a private directory so NeoPi's
// files are only ever read. NeoPi writes while running; a copy is retried until
// both files are unchanged across it, so the snapshot is not torn.
func snapshotDB(profile string) (string, func(), error) {
	src := filepath.Join(profile, DBFileName)
	dir, err := os.MkdirTemp("", "neoproxy-neopi-")
	if err != nil {
		return "", nil, fmt.Errorf("create private snapshot directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dst := filepath.Join(dir, DBFileName)
	for attempt := 0; attempt < 5; attempt++ {
		before := fileStates(src, src+"-wal")
		if before[0] == nil {
			cleanup()
			return "", nil, ErrNotProfile
		}
		_ = os.Remove(dst + "-wal")
		_ = os.Remove(dst + "-shm")
		if err = copyFile(src, dst); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("copy NeoPi credential database: %w", err)
		}
		if before[1] != nil {
			if err = copyFile(src+"-wal", dst+"-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
				cleanup()
				return "", nil, fmt.Errorf("copy NeoPi credential journal: %w", err)
			}
		}
		if sameStates(before, fileStates(src, src+"-wal")) {
			return dst, cleanup, nil
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	cleanup()
	return "", nil, errors.New("NeoPi is writing its credential database continuously; try again in a moment")
}

func fileStates(paths ...string) []os.FileInfo {
	out := make([]os.FileInfo, len(paths))
	for i, p := range paths {
		if info, err := os.Stat(p); err == nil {
			out[i] = info
		}
	}
	return out
}

func sameStates(a, b []os.FileInfo) bool {
	for i := range a {
		if (a[i] == nil) != (b[i] == nil) {
			return false
		}
		if a[i] != nil && (a[i].Size() != b[i].Size() || !a[i].ModTime().Equal(b[i].ModTime())) {
			return false
		}
	}
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
