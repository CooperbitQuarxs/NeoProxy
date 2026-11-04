import { navigate } from "../../app/router";
import type { Session } from "../../api/types";
import { useLedger } from "../people/api";
import { LedgerRow, LedgerSkeleton } from "./Ledger";
import { useLookup } from "./lookup";

/** A person's latest requests, compact; rows open the full story in Activity. */
export function RecentRequests({ session, userId, emptyText, limit = 8 }: { session: Session; userId: string; emptyText: string; limit?: number }) {
	const { lookup } = useLookup(session);
	const ledger = useLedger({ userId: session.role === "admin" ? userId : undefined }, { limit });
	if (ledger.isPending) return <LedgerSkeleton rows={3} />;
	if (ledger.isError) return <p className="pp-inline-note">Couldn't load requests: {(ledger.error as Error).message}. This list retries on its own.</p>;
	const rows = ledger.data ?? [];
	if (!rows.length) return <p className="recent-empty muted">{emptyText}</p>;
	return (
		<ul className="ledger is-compact">
			{rows.map((r) => (
				<LedgerRow key={r.id} r={r} L={lookup} compact onOpen={(x) => navigate(`activity/${encodeURIComponent(x.id)}`)} />
			))}
		</ul>
	);
}
