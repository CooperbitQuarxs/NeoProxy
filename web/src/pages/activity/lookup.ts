import { useMemo } from "react";
import type { Session } from "../../api/types";
import { usePoolModels, useUser, useUsers, useVault, useVirtualModels } from "../people/api";
import { makeLookup } from "./story";

/** Names for people, keys, models and accounts, scoped to what this viewer may see. */
export function useLookup(session: Session) {
	const admin = session.role === "admin";
	const users = useUsers(admin);
	const me = useUser(admin ? undefined : session.userId);
	const vms = useVirtualModels();
	const models = usePoolModels();
	const vault = useVault();
	const people = admin ? users.data : me.data ? [me.data] : undefined;
	const lookup = useMemo(() => makeLookup(people ?? [], vms.data ?? [], models.data ?? [], vault.data), [people, vms.data, models.data, vault.data]);
	return { lookup, people: people ?? [], admin };
}
