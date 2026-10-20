import type { ReactElement, ReactNode } from "react";
import { Archive, CircleHelp, Clock3, LoaderCircle, MessageCircle, SkipForward } from "lucide-react";
import type { WorkDirectory } from "@multica/core/api/workdirs";
import type { AgentTask } from "@multica/core/types";
import { isBuiltInIssueStatus } from "@multica/core/issue-statuses";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { StatusIcon, TaskStatusIcon, useTaskRunStatusLabel } from "@multica/views/issues/components";
import { useIssueStatusLabel } from "@multica/views/issues/hooks";
import { useT } from "@multica/views/i18n";
import { resolveWorkdirLink } from "./workdir-navigation";

type WorkdirStatusProps = {
  row: WorkDirectory;
  serverUrl: string;
  currentServerUrl: string;
};

/**
 * Render a decorative icon beside the readable status without a badge background.
 * @param props Decorative icon and localized text.
 * @returns An inline status with one accessible text label.
 */
function StatusLabel({ icon, children }: { icon: ReactNode; children: ReactNode }): ReactElement {
  return <span className="inline-flex items-center gap-2 whitespace-nowrap"><span aria-hidden="true" className="inline-flex shrink-0">{icon}</span><span>{children}</span></span>;
}

/**
 * Recognize run states without treating future server values as known states.
 * @param status Raw status returned by the local directory inventory.
 * @returns Whether the value belongs to the supported execution status union.
 */
function isRunStatus(status: string): status is AgentTask["status"] {
  return ["queued", "dispatched", "waiting_local_directory", "deferred", "running", "completed", "failed", "cancelled"].includes(status);
}

/**
 * Translate a known execution state and reuse the execution log's terminal icons.
 * @param props Validated execution status.
 * @returns The localized execution status and its icon.
 */
function RunStatus({ status }: { status: AgentTask["status"] }): ReactElement {
  const label = useTaskRunStatusLabel(status);
  const { t } = useT("settings");
  const terminal = status === "completed" || status === "failed" || status === "cancelled";
  const icon = terminal
    ? <TaskStatusIcon status={status} />
    : status === "running"
      ? <LoaderCircle className="size-4 text-warning" />
      : <Clock3 className="size-4 text-muted-foreground" />;
  return <StatusLabel icon={icon}>{status === "deferred" ? t(($) => $.desktop.workdirs.deferred) : label}</StatusLabel>;
}

/**
 * Display a directory's issue, chat, or execution status in the current locale.
 * @param props Directory metadata and service identities used to scope catalog reads.
 * @returns A localized status with the owning workspace's icon and color when available.
 */
export function WorkdirStatus({ row, serverUrl, currentServerUrl }: WorkdirStatusProps): ReactElement {
  const { t } = useT("settings");
  const { t: autopilotText } = useT("autopilots");
  // Other services must never populate this service's workspace-scoped query cache.
  const canReadCatalog = row.kind === "issue" && resolveWorkdirLink(row, serverUrl, currentServerUrl).status === "ready";
  const workspaceId = canReadCatalog ? row.workspaceId : "";
  const catalog = useIssueStatuses(workspaceId);
  const issueLabel = useIssueStatusLabel(workspaceId);
  const status = row.parentStatus;

  if (row.active) {
    return <StatusLabel icon={<LoaderCircle className="size-4 text-warning" />}>{t(($) => $.desktop.workdirs.running)}</StatusLabel>;
  }
  if (row.kind === "issue" && (isBuiltInIssueStatus(status) || catalog.entryOf(status))) {
    return <StatusLabel icon={<StatusIcon status={status} category={catalog.categoryOf(status)} color={catalog.colorOf(status)} icon={catalog.iconOf(status)} />}>{issueLabel(status)}</StatusLabel>;
  }
  if (row.kind === "chat") {
    if (status === "active") return <StatusLabel icon={<MessageCircle className="size-4 text-muted-foreground" />}>{t(($) => $.desktop.workdirs.chat_active)}</StatusLabel>;
    if (status === "archived") return <StatusLabel icon={<Archive className="size-4 text-muted-foreground" />}>{t(($) => $.desktop.workdirs.chat_archived)}</StatusLabel>;
  }
  if (row.kind === "run" || row.kind === "quick_create" || row.kind === "autopilot_run") {
    if (isRunStatus(status)) return <RunStatus status={status} />;
    if (row.kind === "autopilot_run" && status === "skipped") {
      return <StatusLabel icon={<SkipForward className="size-4 text-muted-foreground" />}>{autopilotText(($) => $.run_status.skipped)}</StatusLabel>;
    }
    if (row.kind === "autopilot_run" && status === "issue_created") {
      return <StatusLabel icon={<StatusIcon status="todo" />}>{autopilotText(($) => $.run_status.issue_created)}</StatusLabel>;
    }
  }
  return <StatusLabel icon={<CircleHelp className="size-4 text-muted-foreground" />}>{t(($) => $.desktop.workdirs.unknown)}</StatusLabel>;
}
