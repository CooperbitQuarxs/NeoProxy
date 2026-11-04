package neopool

import "context"

// PlanPrice is an account's monthly plan price as the access dashboard
// resolves it (detection, catalogue, admin override). Priced is false for a
// pay-as-you-go key nobody priced: its catalogue figure is a placeholder.
type PlanPrice struct {
	Kind       string
	MonthlyUSD float64
	Priced     bool
}

// PlanPrices returns the plan price of every account in the snapshot, keyed by
// account ID. It reads no credentials beyond what plan detection already does.
func (s *Store) PlanPrices(ctx context.Context) (map[string]PlanPrice, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]PlanPrice, len(snapshot.Accounts))
	if s.manager == nil || len(snapshot.Accounts) == 0 {
		return out, nil
	}
	rows, err := s.accessRows(ctx)
	if err != nil {
		return nil, err
	}
	for _, account := range snapshot.Accounts {
		a, ok := s.manager.GetByID(account.EngineAuthID)
		if !ok || a == nil {
			continue
		}
		row := rows[account.ID]
		override := planOverride{}
		if row.PlanID.Valid {
			override.PlanID = &row.PlanID.String
		}
		if row.MonthlyUSD.Valid {
			override.MonthlyUSD = &row.MonthlyUSD.Float64
		}
		kind := accessKind(a)
		plan := resolvePlan(accessProviderID(a, kind), kind, a, override)
		out[account.ID] = PlanPrice{Kind: kind, MonthlyUSD: plan.MonthlyUSD, Priced: kind != "apiKey" || plan.ID != "apiKey" || override.MonthlyUSD != nil}
	}
	return out, nil
}
