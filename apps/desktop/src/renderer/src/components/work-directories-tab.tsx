import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Folder, LoaderCircle, RefreshCw, Trash2 } from "lucide-react";
import type { WorkDirectory, WorkdirReport, WorkdirResponse } from "@multica/core/api/workdirs";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  AlertDialog, AlertDialogTrigger, AlertDialogContent, AlertDialogHeader,
  AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel,
} from "@multica/ui/components/ui/alert-dialog";
import { AppLink } from "@multica/views/navigation";
import { resolveWorkdirLink } from "./workdir-navigation";
import { WorkdirStatus } from "./workdir-status";
import { SettingsTab } from "@multica/views/settings";
import { useT } from "@multica/views/i18n";
import { WORKDIR_QUERY_KEY, WORKDIR_OPERATIONS_KEY, useWorkdirOperations, WorkdirOperations, isWorkdirOperationActive, mergeWorkdirOperations } from "./workdir-operations";
import { formatWorkdirSize } from "./workdir-format";

type DirectoryRow = WorkDirectory & { online: boolean; serverUrl: string };
type DirectoryGroup = { id: string; name: string; rows: DirectoryRow[]; sizeBytes: number };

/** Group the local inventory by workspace, retaining each row's controlling profile. */
function groupDirectories(reports: WorkdirReport[], search: string): DirectoryGroup[] {
  const groups = new Map<string, DirectoryGroup>();
  const term = search.trim().toLocaleLowerCase();
  for (const report of reports) {
    for (const directory of report.directories) {
      const text = [directory.path, directory.parentId, directory.parentTitle, directory.workspaceName, directory.workspaceId, directory.parentStatus].join(" ").toLocaleLowerCase();
      if (term && !text.includes(term)) continue;
      const groupId = `${report.serverUrl}/${directory.workspaceId}`;
      let group = groups.get(groupId);
      if (!group) {
        group = { id: groupId, name: directory.workspaceName || directory.workspaceId, rows: [], sizeBytes: 0 };
        groups.set(groupId, group);
      }
      group.rows.push({ ...directory, online: report.online, serverUrl: report.serverUrl });
      group.sizeBytes += directory.sizeBytes;
    }
  }
  for (const group of groups.values()) group.rows.sort((left, right) => right.sizeBytes - left.sizeBytes);
  return Array.from(groups.values()).sort((left, right) => right.sizeBytes - left.sizeBytes);
}

/** Render a verified destination through the desktop navigation adapter. */
function WorkDirectoryTaskLink({ row }: { row: DirectoryRow }) {
  const { t } = useT("settings");
  const config = window.desktopAPI.runtimeConfig;
  const link = resolveWorkdirLink(row, row.serverUrl, config.ok ? config.config.apiUrl : "");
  const label = <><div>{row.parentTitle || (row.parentId ? row.kind : t(($) => $.desktop.workdirs.unlinked))}</div><div className="mt-1 break-all font-mono">{row.parentId}</div></>;
  if (link.status === "ready") {
    return <AppLink href={link.href} className="block rounded-sm text-primary underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">{label}</AppLink>;
  }
  return <><div>{label}</div>{row.parentId && <p className="mt-1 text-muted-foreground">{link.status === "other_server" ? t(($) => $.desktop.workdirs.other_server) : t(($) => $.desktop.workdirs.link_unavailable)}</p>}</>;
}

/** Render local directory inventory and explicit, server-checked cleanup controls. */
export function WorkDirectoriesTab() {
  const { t } = useT("settings");
  const queryClient = useQueryClient();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<DirectoryRow | null>(null);
  const inventory = useQuery({
    queryKey: WORKDIR_QUERY_KEY,
    queryFn: (): Promise<WorkdirResponse> => window.daemonAPI.workdirs({ action: "list" }),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const operations = useWorkdirOperations();
  const refresh = () => queryClient.invalidateQueries({ queryKey: WORKDIR_QUERY_KEY });
  /**
   * Keep accepted jobs visible immediately, including across settings navigation.
   * @param response Acknowledged daemon operations.
   * @returns Completion of the cache update, without awaiting deletion.
   */
  const acceptOperations = async (response: WorkdirResponse): Promise<void> => {
    await queryClient.cancelQueries({ queryKey: WORKDIR_OPERATIONS_KEY });
    queryClient.setQueryData<WorkdirResponse>(WORKDIR_OPERATIONS_KEY, (previous) => ({
      ...response, operations: mergeWorkdirOperations(previous?.operations ?? [], response.operations),
    }));
    void queryClient.invalidateQueries({ queryKey: WORKDIR_OPERATIONS_KEY });
  };
  const cleanup = useMutation({
    mutationFn: (): Promise<WorkdirResponse> => window.daemonAPI.workdirs({ action: "start-reclaim" }),
    onSuccess: acceptOperations,
  });
  const deletion = useMutation({
    mutationFn: (directory: DirectoryRow): Promise<WorkdirResponse> => window.daemonAPI.workdirs({ action: "start-delete", profile: directory.profile, id: directory.id }),
    onSuccess: (response) => { setSelected(null); return acceptOperations(response); },
  });
  const reports = inventory.data?.reports;
  const groups = useMemo(() => groupDirectories(reports ?? [], search), [reports, search]);
  const activeOperations = operations.data?.operations.filter(isWorkdirOperationActive) ?? [];
  const problems = [
    ...(inventory.data?.result.problems ?? []),
    ...(reports?.flatMap((report) => report.problems) ?? []),
    ...(cleanup.data?.result.problems ?? []),
    ...(operations.data?.result.problems ?? []),
  ];
  const totalBytes = reports?.flatMap((report) => report.directories).reduce((sum, row) => sum + row.sizeBytes, 0) ?? 0;
  const totalCount = reports?.reduce((sum, report) => sum + report.directories.length, 0) ?? 0;
  const canCleanup = reports?.some((report) => report.online) === true;

  return (
    <SettingsTab title={t(($) => $.desktop.workdirs.title)} scope="device">
      <div className="flex flex-wrap items-center justify-between gap-3 py-4">
        <div>
          <div className="text-display-sm font-semibold tabular-nums">{formatWorkdirSize(totalBytes)}</div>
          <div className="text-caption text-muted-foreground">{t(($) => $.desktop.workdirs.count, { count: totalCount })}</div>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" disabled={inventory.isFetching} onClick={() => void refresh()} aria-busy={inventory.isFetching}>
            <RefreshCw className="size-4" />{t(($) => $.desktop.workdirs.refresh)}
          </Button>
          <Button size="sm" disabled={cleanup.isPending || activeOperations.some((job) => job.action === "reclaim") || !canCleanup} onClick={() => cleanup.mutate()} aria-busy={cleanup.isPending}>
            {cleanup.isPending ? <LoaderCircle className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
            {t(($) => $.desktop.workdirs.reclaim)}
          </Button>
        </div>
      </div>
      <p className="mb-4 text-caption text-muted-foreground">{t(($) => $.desktop.workdirs.policy)}</p>
      {(inventory.error || cleanup.error || operations.error) && <p role="alert" className="mb-4 break-words text-caption text-destructive">{(inventory.error ?? cleanup.error ?? operations.error)?.message}</p>}
      <WorkdirOperations operations={operations.data?.operations ?? []} directories={reports?.flatMap((report) => report.directories) ?? []} />
      {problems.length > 0 && (
        <details className="mb-4 rounded-md border p-3 text-caption">
          <summary className="cursor-pointer font-medium">{t(($) => $.desktop.workdirs.problems, { count: problems.length })}</summary>
          <ul className="mt-2 space-y-2">{problems.map((problem, index) => <li key={`${problem.path}-${index}`}><div className="break-all font-mono">{problem.path}</div><div className="break-words text-muted-foreground">{problem.reason}</div></li>)}</ul>
        </details>
      )}
      <Input aria-label={t(($) => $.desktop.workdirs.search)} placeholder={t(($) => $.desktop.workdirs.search)} value={search} onChange={(event) => setSearch(event.target.value)} className="mb-4" />
      {inventory.isPending && <p role="status" className="py-8 text-center text-muted-foreground">{t(($) => $.desktop.workdirs.loading)}</p>}
      {!inventory.isPending && !inventory.error && groups.length === 0 && <p className="py-8 text-center text-muted-foreground">{t(($) => $.desktop.workdirs.empty)}</p>}
      <AlertDialog open={selected !== null} onOpenChange={(open) => { if (!open && !deletion.isPending) setSelected(null); }}>
        <div className="space-y-6">
          {groups.map((group) => (
            <section key={group.id} aria-label={group.name}>
              <div className="mb-2 flex items-center gap-2"><Folder className="size-4 shrink-0 text-muted-foreground" /><h3 className="min-w-0 break-words text-body font-medium">{group.name}</h3><span className="ml-auto shrink-0 text-caption tabular-nums text-muted-foreground">{formatWorkdirSize(group.sizeBytes)}</span></div>
              <div className="overflow-x-auto rounded-md border">
                <table className="w-full text-left text-caption">
                  <thead className="bg-muted/40 text-muted-foreground"><tr>
                    <th className="px-3 py-2 font-medium">{t(($) => $.desktop.workdirs.directory)}</th>
                    <th className="px-3 py-2 font-medium">{t(($) => $.desktop.workdirs.task)}</th>
                    <th className="px-3 py-2 font-medium">{t(($) => $.desktop.workdirs.status)}</th>
                    <th className="px-3 py-2 text-right font-medium">{t(($) => $.desktop.workdirs.size)}</th>
                    <th className="px-3 py-2 font-medium">{t(($) => $.desktop.workdirs.actions)}</th>
                  </tr></thead>
                  <tbody>{group.rows.map((row) => (
                    <tr key={row.path} className="border-t align-top">
                      <td className="min-w-48 max-w-sm px-3 py-3"><div className="break-all font-mono">{row.path}</div><div className="mt-1 text-muted-foreground">{row.mode === "script" ? t(($) => $.desktop.workdirs.script) : t(($) => $.desktop.workdirs.managed)} · {row.profile || t(($) => $.desktop.workdirs.default_profile)}</div>{(!row.online || row.blockedReason) && <p className="mt-2 break-words text-muted-foreground">{row.blockedReason || t(($) => $.desktop.workdirs.offline)}</p>}</td>
                      <td className="min-w-32 max-w-xs break-words px-3 py-3"><WorkDirectoryTaskLink row={row} /></td>
                      <td className="px-3 py-3"><WorkdirStatus row={row} serverUrl={row.serverUrl} currentServerUrl={window.desktopAPI.runtimeConfig.ok ? window.desktopAPI.runtimeConfig.config.apiUrl : ""} /></td>
                      <td className="whitespace-nowrap px-3 py-3 text-right tabular-nums">{formatWorkdirSize(row.sizeBytes)}</td>
                      <td className="px-3 py-3"><AlertDialogTrigger render={<Button variant="outline" size="xs" disabled={deletion.isPending || activeOperations.some((job) => job.profile === row.profile && job.action === "delete" && job.directoryId === row.id) || !row.online || row.active || row.blockedReason !== ""} onClick={() => { deletion.reset(); setSelected(row); }} aria-label={t(($) => $.desktop.workdirs.delete_label, { path: row.path })} />}>{t(($) => $.desktop.workdirs.delete)}</AlertDialogTrigger></td>
                    </tr>
                  ))}</tbody>
                </table>
              </div>
            </section>
          ))}
        </div>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.desktop.workdirs.confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.desktop.workdirs.confirm_description)}</AlertDialogDescription>
          </AlertDialogHeader>
          <p className="break-all font-mono text-caption">{selected?.path}</p>
          {deletion.error && <p role="alert" className="break-words text-caption text-destructive">{deletion.error.message}</p>}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deletion.isPending}>{t(($) => $.desktop.workdirs.cancel)}</AlertDialogCancel>
            <Button variant="destructive" disabled={deletion.isPending || selected === null} aria-busy={deletion.isPending} onClick={() => { if (selected !== null) deletion.mutate(selected); }}>{deletion.isPending && <LoaderCircle className="size-4 animate-spin" />}{t(($) => $.desktop.workdirs.delete)}</Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
