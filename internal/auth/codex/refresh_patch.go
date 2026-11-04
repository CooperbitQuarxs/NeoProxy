package codex

import "reflect"

// CredentialRefreshPatch carries a version-checked credential refresh or disable.
// Memory and disk baselines let publication reconcile a peer's persisted grant.
type CredentialRefreshPatch struct {
	before     map[string]any
	diskBefore map[string]any
	after      map[string]any
	disable    bool
}

// NewCredentialRefreshPatch records the in-memory baseline (before), the on-disk
// baseline (diskBefore) observed under the file lock, and the refreshed grant (after).
func NewCredentialRefreshPatch(before, diskBefore, after map[string]any) *CredentialRefreshPatch {
	patch := &CredentialRefreshPatch{
		before: credentialFields(before),
		after:  credentialFields(after),
	}
	if len(diskBefore) > 0 {
		patch.diskBefore = credentialFields(diskBefore)
	}
	return patch
}

// NewCredentialDisablePatch disables only the persisted grant actually attempted.
// before is the caller's memory snapshot, which may predate the locked disk read.
func NewCredentialDisablePatch(before, attempted map[string]any) *CredentialRefreshPatch {
	return &CredentialRefreshPatch{
		before:     credentialFields(before),
		diskBefore: credentialFields(attempted),
		disable:    true,
	}
}

func credentialFields(metadata map[string]any) map[string]any {
	fields := make(map[string]any)
	for _, key := range codexCredentialKeys {
		if value, ok := metadata[key]; ok {
			fields[key] = value
		}
	}
	return cloneStringAnyMap(fields)
}

func sameCredentialVersion(a, b map[string]any) bool {
	for _, key := range codexCredentialKeys {
		if !reflect.DeepEqual(a[key], b[key]) {
			return false
		}
	}
	return true
}

func matchesBaselineOrAfter(target, memBefore, diskBefore, after map[string]any) (matched bool, effectiveBefore map[string]any) {
	if len(diskBefore) > 0 && sameCredentialVersion(target, diskBefore) {
		return true, diskBefore
	}
	if sameCredentialVersion(target, memBefore) {
		return true, memBefore
	}
	if len(after) > 0 && sameCredentialVersion(target, after) {
		return true, after
	}
	return false, nil
}

// IsCodexCredentialKey reports whether key belongs to the credential managed by CredentialRefreshPatch.
func IsCodexCredentialKey(key string) bool {
	for _, k := range codexCredentialKeys {
		if k == key {
			return true
		}
	}
	return false
}

// CanApplyTo reports whether a pending mutation can still publish to this snapshot.
func (p *CredentialRefreshPatch) CanApplyTo(disk map[string]any) bool {
	if p == nil {
		return false
	}
	ok, _ := matchesBaselineOrAfter(disk, p.before, p.diskBefore, p.after)
	return ok
}

// Apply overlays a refresh or disables the attempted grant on the locked snapshot.
// A disable never matches an older memory baseline: only the attempted disk version.
func (p *CredentialRefreshPatch) Apply(disk map[string]any) map[string]any {
	merged := cloneStringAnyMap(disk)
	if merged == nil {
		merged = make(map[string]any)
	}
	if p.disable {
		if sameCredentialVersion(disk, p.diskBefore) {
			merged["disabled"] = true
		}
		return merged
	}
	ok, base := matchesBaselineOrAfter(disk, p.before, p.diskBefore, p.after)
	if !ok {
		return merged
	}
	for key, value := range p.after {
		// Preserve fields changed on disk that the refresh did not modify.
		if oldValue, existed := base[key]; !existed || !reflect.DeepEqual(oldValue, value) {
			merged[key] = value
		}
	}
	return merged
}
