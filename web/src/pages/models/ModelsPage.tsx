import type { PageProps } from "../../app/App";
import { navigate } from "../../app/router";
import { Button, Notice, PageHeader, Skeleton, TabPanel, Tabs } from "../../ui/components";
import { Catalogue } from "./Catalogue";
import { useModels, useRequestCounts, useUsers, useVault, useVirtualModels } from "./data";
import { Editor } from "./Editor";
import { MemberModels } from "./MemberModels";
import { VirtualList } from "./VirtualList";
import "./models.css";

export function ModelsPage({ session, params, query }: PageProps) {
	if (session.role !== "admin") return <MemberModels session={session} />;
	if (params[0]) return <Editor id={decodeURIComponent(params[0])} session={session} />;
	return <ModelsHome view={query.get("view") === "providers" ? "providers" : "virtual"} />;
}

function ModelsHome({ view }: { view: "virtual" | "providers" }) {
	const models = useModels();
	const vault = useVault();
	const vms = useVirtualModels();
	const users = useUsers();
	const counts = useRequestCounts();
	const loading = models.isPending || vault.isPending || vms.isPending;
	const error = models.error || vault.error || vms.error;
	const providers = vault.data?.providers ?? [];
	const served = (models.data ?? []).filter((m) => m.exposed && providers.find((p) => p.id === m.providerId)?.exposed).length;
	const create = () => navigate("models/new");

	return (
		<>
			<PageHeader
				title="Models"
				lede={
					<>
						A virtual model is a stable name, like <code>neoproxy/office-default</code>, that people put in their clients. NeoProxy answers it with real provider models in the order you choose and falls back when one runs out, so you can swap providers without anyone changing their setup.
					</>
				}
				actions={
					served > 0 && (vms.data?.length ?? 0) > 0 ? (
						<Button variant="primary" icon="plus" onClick={create}>
							New virtual model
						</Button>
					) : undefined
				}
			/>

			{error && (
				<Notice tone="bad" title="Couldn't load models">
					{(error as Error).message}. NeoProxy may be restarting; this page retries on its own.
				</Notice>
			)}

			<Tabs
				value={view}
				onValueChange={(v) => navigate(v === "providers" ? "models?view=providers" : "models")}
				items={[
					{ value: "virtual", label: "Virtual models", count: vms.data?.length },
					{ value: "providers", label: "Provider models", count: models.data?.length },
				]}
			>
				<TabPanel value="virtual">
					{loading ? (
						<LoadingCards />
					) : (
						!error && (
							<VirtualList vms={vms.data ?? []} models={models.data ?? []} vault={vault.data!} users={users.data ?? []} counts={counts.data} onCreate={create} canCreate={served > 0} />
						)
					)}
				</TabPanel>
				<TabPanel value="providers">
					{loading ? <LoadingCards /> : !error && <Catalogue models={models.data ?? []} vault={vault.data!} vms={vms.data ?? []} users={users.data ?? []} counts={counts.data} />}
				</TabPanel>
			</Tabs>
		</>
	);
}

function LoadingCards() {
	return (
		<div className="stack" aria-busy="true">
			<Skeleton h={38} w={360} r={10} />
			<Skeleton h={150} r={14} />
			<Skeleton h={150} r={14} />
		</div>
	);
}
