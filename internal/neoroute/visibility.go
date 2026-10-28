package neoroute

import (
	"context"
	"slices"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

// VisibleAccountIDs supplies B's member vault projection. It follows current
// grants and exposed stage pools, never all credentials in a provider by default.
func (s *Store) VisibleAccountIDs(ctx context.Context, p neo.Principal) ([]string, error) {
	p, err := s.refresh(ctx, p)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.pools.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	virtuals, err := s.VirtualModels(ctx)
	if err != nil {
		return nil, err
	}
	visible := make(map[string]bool)
	addPool := func(model string, scope AccountScope) {
		m, ok := exposedModel(snapshot, model)
		if !ok {
			return
		}
		for _, a := range snapshot.Accounts {
			if a.ProviderID == m.ProviderID && accountAllowed(p, a) && accountSupports(a, model) && (scope.All || slices.Contains(scope.IDs, a.ID)) {
				visible[a.ID] = true
			}
		}
	}
	for _, vm := range virtuals {
		if _, ok := grantFor(p, vm.ID); !ok {
			continue
		}
		for _, stage := range vm.Stages {
			addPool(stage.Model, stage.Accounts)
		}
		if vm.Terminal.Kind == "passthrough" {
			addPool(vm.Terminal.Model, AccountScope{All: true})
		}
	}
	for _, model := range snapshot.Models {
		if slices.Contains(p.ProviderAccess, model.ProviderID) {
			addPool(model.ID, AccountScope{All: true})
		}
	}
	ids := make([]string, 0, len(visible))
	for id := range visible {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
