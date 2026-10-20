import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { UseQueryResult } from "@tanstack/react-query";
import { CircleCheck, CircleX, LoaderCircle } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import type { ReactElement } from "react";
import type { WorkDirectory, WorkdirOperation, WorkdirResponse } from "@multica/core/api/workdirs";
import { Button } from "@multica/ui/components/ui/button";
import { formatWorkdirSize } from "./workdir-format";
import { useT } from "@multica/views/i18n";

export const WORKDIR_QUERY_KEY = ["desktop", "work-directories"];
export const WORKDIR_OPERATIONS_KEY = ["desktop", "work-directory-operations"];
const ACTIVE_POLL_MS = 1000;
const IDLE_POLL_MS = 10000;
const MILLISECONDS_PER_SECOND = 1000;
const SUCCESS_NOTICE_MS = 5000;

type OperationCardProps = {
  operation: WorkdirOperation;
  directories: WorkDirectory[];
  onArchive?: (key: string) => void;
};

/**
 * Identify a cleanup record across independently managed daemon profiles.
 * @param operation Daemon-reported operation.
 * @returns Profile-scoped record key.
 */
function operationKey(operation: WorkdirOperation): string {
  return `${operation.profile}/${operation.id}`;
}

/**
 * Return whether an acknowledged operation still owns a queue position.
 * @param operation Daemon-reported operation.
 * @returns Whether work is queued or running.
 */
export function isWorkdirOperationActive(operation: WorkdirOperation): boolean {
  return operation.status === "queued" || operation.status === "running";
}

/**
 * Merge acknowledged jobs without dropping another profile's queued work.
 * @param previous Previously observed operations.
 * @param incoming Newly acknowledged or polled operations.
 * @returns Operations keyed by profile and operation ID.
 */
export function mergeWorkdirOperations(previous: WorkdirOperation[], incoming: WorkdirOperation[]): WorkdirOperation[] {
  const jobs = new Map(previous.map((job) => [`${job.profile}/${job.id}`, job]));
  for (const job of incoming) jobs.set(`${job.profile}/${job.id}`, job);
  return Array.from(jobs.values());
}

/** Poll daemon-owned jobs independently of expensive directory inventory reads.
 * Completed deletion updates cached rows only after the daemon confirms success.
 * @returns The shared operation query, including loading and error states.
 */
export function useWorkdirOperations(): UseQueryResult<WorkdirResponse, Error> {
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: WORKDIR_OPERATIONS_KEY,
    queryFn: async (): Promise<WorkdirResponse> => {
      const previous = queryClient.getQueryData<WorkdirResponse>(WORKDIR_OPERATIONS_KEY);
      const response = await window.daemonAPI.workdirs({ action: "operations" });
      const finished = response.operations.filter((job) => !isWorkdirOperationActive(job) &&
        previous?.operations.some((old) => old.id === job.id && old.profile === job.profile && isWorkdirOperationActive(old)));
      const deleted = finished.filter((job) => job.action === "delete" && job.status === "succeeded");
      if (deleted.length > 0) {
        await queryClient.cancelQueries({ queryKey: WORKDIR_QUERY_KEY });
        queryClient.setQueryData<WorkdirResponse>(WORKDIR_QUERY_KEY, (inventory) => inventory && ({
          ...inventory,
          reports: inventory.reports.map((report) => ({
            ...report,
            directories: report.directories.filter((row) => !deleted.some((job) => job.profile === row.profile && job.directoryId === row.id)),
          })),
        }));
      }
      if (finished.length > 0 && !response.operations.some(isWorkdirOperationActive)) {
        void queryClient.invalidateQueries({ queryKey: WORKDIR_QUERY_KEY });
      }
      return response.result.problems.length > 0 && previous
        ? { ...response, operations: mergeWorkdirOperations(previous.operations, response.operations) }
        : response;
    },
    refetchInterval: (query) => query.state.data?.operations.some(isWorkdirOperationActive) ? ACTIVE_POLL_MS : IDLE_POLL_MS,
    refetchIntervalInBackground: true,
    retry: false,
  });
}

/**
 * Keep live progress and unacknowledged results above a collapsed history.
 * @param props Operations from the owning daemons.
 * @returns Active notices and accessible history, or nothing without records.
 */
export function WorkdirOperations({ operations, directories }: { operations: WorkdirOperation[]; directories: WorkDirectory[] }): ReactElement | null {
  const { t } = useT("settings");
  const [archivedKeys, setArchivedKeys] = useState<ReadonlySet<string>>(() => new Set());
  const archive = useCallback((key: string): void => {
    setArchivedKeys((previous) => new Set([...previous, key]));
  }, []);
  if (operations.length === 0) return null;
  const notices: WorkdirOperation[] = [];
  const history: WorkdirOperation[] = [];
  for (const operation of [...operations].reverse()) {
    if (!isWorkdirOperationActive(operation) && archivedKeys.has(operationKey(operation))) {
      history.push(operation);
    } else {
      notices.push(operation);
    }
  }
  return (
    <>
      {notices.length > 0 && (
        <section aria-label={t(($) => $.desktop.workdirs.operations)} className="mb-4 space-y-2">
          <h3 className="text-body font-medium">{t(($) => $.desktop.workdirs.operations)}</h3>
          {notices.map((operation) => (
            <WorkdirOperationCard key={operationKey(operation)} operation={operation} directories={directories} onArchive={archive} />
          ))}
        </section>
      )}
      {history.length > 0 && (
        <details className="mb-4 rounded-md border p-3 text-caption">
          <summary className="cursor-pointer">{t(($) => $.desktop.workdirs.operation_history, { count: history.length })}</summary>
          <div className="mt-2 max-h-80 space-y-2 overflow-y-auto">
            {history.map((operation) => (
              <WorkdirOperationCard key={operationKey(operation)} operation={operation} directories={directories} />
            ))}
          </div>
        </details>
      )}
    </>
  );
}

/**
 * Render one cleanup result and archive clean success after its notice period.
 * Timer dependencies stay stable across polling; warnings require acknowledgement.
 * @param props Operation, directory labels and optional notice archive callback.
 * @returns Measured progress or the retained result with its diagnostics.
 */
function WorkdirOperationCard({ operation, directories, onArchive }: OperationCardProps): ReactElement {
  const { t } = useT("settings");
  const key = operationKey(operation);
  const cleanSuccess = operation.status === "succeeded" && operation.error === "" &&
    operation.result.skipped === 0 && operation.result.problems.length === 0;
  useEffect(() => {
    if (!cleanSuccess || !onArchive) return;
    const timer = setTimeout(() => onArchive(key), SUCCESS_NOTICE_MS);
    return () => clearTimeout(timer);
  }, [cleanSuccess, key, onArchive]);
  const stages: Record<WorkdirOperation["stage"], string> = {
    queued: t(($) => $.desktop.workdirs.phase_queued),
    locating: t(($) => $.desktop.workdirs.phase_locating),
    measuring: t(($) => $.desktop.workdirs.phase_measuring),
    checking: t(($) => $.desktop.workdirs.phase_checking),
    remote: t(($) => $.desktop.workdirs.phase_remote),
    saving: t(($) => $.desktop.workdirs.phase_saving),
    deleting: t(($) => $.desktop.workdirs.phase_deleting),
    maintenance: t(($) => $.desktop.workdirs.phase_maintenance),
    unknown: t(($) => $.desktop.workdirs.unknown),
  };
  const directory = directories.find((row) => row.profile === operation.profile && row.id === operation.directoryId);
  const active = isWorkdirOperationActive(operation);
  const completed = operation.status === "succeeded";
  const label = active ? stages[operation.stage] : completed ? t(($) => $.desktop.workdirs.operation_completed) : operation.status === "failed" ? t(($) => $.desktop.workdirs.operation_failed) : t(($) => $.desktop.workdirs.unknown);
  return (
    <div key={`${operation.profile}/${operation.id}`} className="rounded-md border p-3 text-caption">
      <div className="flex flex-wrap items-center gap-2">
        {active ? <LoaderCircle aria-hidden="true" className="size-4 animate-spin" /> : completed ? <CircleCheck aria-hidden="true" className="size-4 text-success" /> : <CircleX aria-hidden="true" className="size-4 text-destructive" />}
        <span className="font-medium">{operation.action === "delete" ? t(($) => $.desktop.workdirs.delete) : t(($) => $.desktop.workdirs.reclaim)}</span>
        <span role="status">{label}</span>
        <span className="ml-auto tabular-nums text-muted-foreground">{t(($) => $.desktop.workdirs.elapsed, { seconds: Math.floor(operation.elapsedMs / MILLISECONDS_PER_SECOND) })}</span>
        {!active && onArchive && <Button variant="ghost" size="xs" onClick={() => onArchive(key)}>{t(($) => $.desktop.workdirs.archive_operation)}</Button>}
      </div>
      <p className="mt-1 break-all text-muted-foreground">{operation.path || directory?.path || operation.profile || t(($) => $.desktop.workdirs.default_profile)}</p>
      {active && operation.currentPath && operation.currentPath !== (operation.path || directory?.path) && <p className="mt-1 break-all font-mono">{operation.currentPath}</p>}
      {active && <p className="mt-1 text-muted-foreground">{t(($) => $.desktop.workdirs.stage_elapsed, { seconds: Math.floor(operation.stageElapsedMs / MILLISECONDS_PER_SECOND) })}</p>}
      {operation.removedFiles > 0 && <p className="mt-1 tabular-nums">{t(($) => $.desktop.workdirs.removed_files, { count: operation.removedFiles })}</p>}
      {(!active || operation.action === "reclaim") && <p className="mt-1">{t(($) => $.desktop.workdirs.result, { removed: operation.result.removed, skipped: operation.result.skipped, size: formatWorkdirSize(operation.result.reclaimedBytes) })}</p>}
      {operation.error && <p role="alert" className="mt-2 break-words text-destructive">{operation.error}</p>}
      {operation.result.problems.length > 0 && <details className="mt-2"><summary>{t(($) => $.desktop.workdirs.problems, { count: operation.result.problems.length })}</summary><ul className="mt-2 space-y-1">{operation.result.problems.map((problem, index) => <li key={`${problem.path}/${index}`} className="break-words">{problem.path}: {problem.reason}</li>)}</ul></details>}
    </div>
  );
}
