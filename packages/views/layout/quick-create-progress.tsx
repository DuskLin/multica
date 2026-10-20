"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { taskMessagesOptions } from "@multica/core/chat/queries";
import type { AgentTask } from "@multica/core/types/agent";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import { TranscriptButton } from "../common/task-transcript/transcript-button";
import { buildTimeline } from "../common/task-transcript/build-timeline";
import { useT } from "../i18n";

const activeStatuses = new Set(["queued", "dispatched", "waiting_local_directory", "running"]);

function isOwnCreation(task: AgentTask, userId: string | undefined) {
  return !!userId && task.kind === "quick_create" && task.attribution?.initiator?.id === userId;
}

export function QuickCreateProgress({ wsId }: { wsId: string }) {
  const userId = useAuthStore((state) => state.user?.id);
  const { data: tasks = [] } = useQuery({
    ...agentTaskSnapshotOptions(wsId),
    refetchInterval: (query) => query.state.data?.some(
      (task) => isOwnCreation(task, userId) && activeStatuses.has(task.status),
    ) ? 2000 : false,
  });
  const observed = useRef(new Set<string>());
  const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
  useEffect(() => {
    for (const task of tasks) {
      if (isOwnCreation(task, userId) && activeStatuses.has(task.status)) {
        observed.current.add(task.id);
      }
    }
  }, [tasks, userId]);
  const visible = tasks.filter((task) =>
    isOwnCreation(task, userId) && !dismissed.has(task.id) && (
      activeStatuses.has(task.status) ||
      (observed.current.has(task.id) && (task.status === "failed" || task.status === "cancelled"))
    ),
  );
  if (visible.length === 0) return null;
  return (
    <aside className="fixed bottom-4 left-1/2 z-40 max-h-[50vh] w-[min(36rem,calc(100vw-2rem))] -translate-x-1/2 space-y-2 overflow-y-auto rounded-lg border bg-popover p-3 text-popover-foreground shadow-lg">
      {visible.map((task) => (
        <QuickCreateRun
          key={task.id}
          task={task}
          onDismiss={() => setDismissed((prev) => new Set(prev).add(task.id))}
        />
      ))}
    </aside>
  );
}

function QuickCreateRun({ task, onDismiss }: { task: AgentTask; onDismiss: () => void }) {
  const { t } = useT("issues");
  const live = activeStatuses.has(task.status);
  const [transcriptOpen, setTranscriptOpen] = useState(false);
  // Quick-create runs have no issue yet, so their messages are backfilled
  // directly instead of depending on issue-scoped websocket broadcasts.
  const { data: messages = [], isError, refetch } = useQuery({
    ...taskMessagesOptions(task.id),
    refetchInterval: (query) => live && (
      transcriptOpen || (
        task.status !== "running" && !query.state.data?.some(
          (message) => message.tool === "Workspace initialization" && message.type === "tool_result",
        )
      )
    ) ? 1000 : false,
    refetchOnMount: "always",
  });
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [live]);
  const started = messages.some((message) =>
    message.tool === "Workspace initialization" || message.tool === "Initialization output",
  );
  const ready = messages.some((message) => message.tool === "Workspace initialization" && message.type === "tool_result");
  const status = !live ? t(($) => $.creation_progress.failed)
    : started && !ready ? t(($) => $.creation_progress.initializing)
      : task.status === "queued" ? t(($) => $.creation_progress.queued)
        : task.status === "waiting_local_directory" ? t(($) => $.creation_progress.waiting_directory)
          : task.status === "dispatched" ? t(($) => $.creation_progress.preparing)
            : t(($) => $.creation_progress.creating);
  const output = messages
    .filter((message) => message.tool === "Initialization output" || (
      message.tool === "Workspace initialization" && message.type === "tool_result"
    ))
    .map((message) => message.tool === "Workspace initialization"
      ? `\n${message.output ?? ""}` : message.output ?? "")
    .join("");
  const seconds = Math.max(0, Math.floor((now - Date.parse(task.dispatched_at ?? task.created_at)) / 1000)) || 0;
  const logRef = useRef<HTMLPreElement>(null);
  const following = useRef(true);
  useEffect(() => {
    if (following.current && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [output]);
  useEffect(() => {
    if (!live || task.status === "running") void refetch();
  }, [live, task.status, refetch]);
  return (
    <section className="min-w-0 space-y-2">
      <div className="flex items-center gap-2 text-body" role="status">
        {live && <Loader2 aria-hidden="true" className="size-4 shrink-0 animate-spin" />}
        <span>{status}</span>
        {live && (
          <span aria-hidden="true" className="tabular-nums text-muted-foreground">
            {Math.floor(seconds / 60)}:{String(seconds % 60).padStart(2, "0")}
          </span>
        )}
        <TranscriptButton
          task={task}
          agentName=""
          items={buildTimeline(messages)}
          isLive={task.status === "running" || task.status === "dispatched"}
          open={transcriptOpen}
          onOpenChange={setTranscriptOpen}
          title={t(($) => $.execution_log.transcript_tooltip)}
        />
        {!live && (
          <Button variant="ghost" size="sm" onClick={onDismiss}>
            {t(($) => $.creation_progress.dismiss)}
          </Button>
        )}
      </div>
      {task.trigger_summary && <p className="truncate text-body text-muted-foreground" title={task.trigger_summary}>{task.trigger_summary}</p>}
      {started && (
        <details open>
          <summary className="cursor-pointer text-caption text-muted-foreground">
            {t(($) => $.creation_progress.output)}
          </summary>
          <pre
            ref={logRef}
            onScroll={() => {
              const el = logRef.current;
              if (el) following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 32;
            }}
            className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-sm bg-muted p-2 font-mono text-caption"
          >
            {output.slice(-16000) || t(($) => $.creation_progress.waiting_output)}
          </pre>
          {output.length > 16000 && (
            <p className="text-caption text-muted-foreground">{t(($) => $.creation_progress.recent_output)}</p>
          )}
        </details>
      )}
      {isError && <p className="text-caption text-destructive">{t(($) => $.creation_progress.reconnecting)}</p>}
      {!live && task.error && <p className="whitespace-pre-wrap break-words text-caption text-destructive">{task.error}</p>}
    </section>
  );
}
