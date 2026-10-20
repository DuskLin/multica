import { z } from "zod";
import { parseWithFallback } from "./schema";

const problemSchema = z.object({ path: z.string(), reason: z.string() });
const directorySchema = z.object({
  id: z.string().min(1),
  profile: z.string(),
  workspace_id: z.string(),
  workspace_name: z.string().default(""),
  workspace_slug: z.string().default(""),
  target_kind: z.enum(["", "issue", "chat"]).catch(""),
  target_id: z.string().default(""),
  parent_id: z.string().default(""),
  parent_title: z.string().default(""),
  parent_status: z.string().default(""),
  kind: z.string().default("unknown"),
  mode: z.string().default("unknown"),
  path: z.string().min(1),
  size_bytes: z.number().nonnegative(),
  active: z.boolean().default(false),
  blocked_reason: z.string().default(""),
}).transform((row) => ({
  id: row.id, profile: row.profile, workspaceId: row.workspace_id,
  workspaceName: row.workspace_name, workspaceSlug: row.workspace_slug,
  targetKind: row.target_kind, targetId: row.target_id, parentId: row.parent_id,
  parentTitle: row.parent_title, parentStatus: row.parent_status,
  kind: row.kind, mode: row.mode, path: row.path, sizeBytes: row.size_bytes,
  active: row.active, blockedReason: row.blocked_reason,
}));
const actionResultSchema = z.object({
  removed: z.number().int().nonnegative(),
  skipped: z.number().int().nonnegative(),
  reclaimed_bytes: z.number().nonnegative(),
  problems: z.array(problemSchema).default([]),
}).transform((result) => ({
  removed: result.removed, skipped: result.skipped,
  reclaimedBytes: result.reclaimed_bytes, problems: result.problems,
}));
const reportSchema = z.object({
  profile: z.string(), root: z.string(), server_url: z.string().default(""), online: z.boolean().default(false),
  directories: z.array(directorySchema), problems: z.array(problemSchema).default([]),
}).transform(({ server_url, ...report }) => ({ ...report, serverUrl: server_url }));
const operationSchema = z.object({
  id: z.string().min(1),
  profile: z.string(),
  directory_id: z.string(),
  action: z.enum(["delete", "reclaim"]),
  status: z.enum(["queued", "running", "succeeded", "failed", "unknown"]).catch("unknown"),
  stage: z.enum(["queued", "locating", "measuring", "checking", "remote", "saving", "deleting", "maintenance", "unknown"]).catch("unknown"),
  path: z.string().default(""),
  current_path: z.string().default(""),
  elapsed_ms: z.number().nonnegative(),
  stage_elapsed_ms: z.number().nonnegative(),
  removed_files: z.number().int().nonnegative(),
  result: actionResultSchema,
  error: z.string().default(""),
}).transform((operation) => ({
  id: operation.id, profile: operation.profile, directoryId: operation.directory_id,
  action: operation.action, status: operation.status, stage: operation.stage,
  path: operation.path, currentPath: operation.current_path,
  elapsedMs: operation.elapsed_ms, stageElapsedMs: operation.stage_elapsed_ms,
  removedFiles: operation.removed_files, result: operation.result, error: operation.error,
}));
const responseSchema = z.object({
  reports: z.array(reportSchema), result: actionResultSchema,
  operations: z.array(operationSchema).default([]),
});
const commandSchema = z.discriminatedUnion("action", [
  z.object({ action: z.literal("list") }).strict(),
  z.object({ action: z.literal("reclaim") }).strict(),
  z.object({ action: z.literal("delete"), profile: z.string(), id: z.string().regex(/^[a-f0-9]{64}$/) }).strict(),
  z.object({ action: z.literal("operations") }).strict(),
  z.object({ action: z.literal("start-reclaim") }).strict(),
  z.object({ action: z.literal("start-delete"), profile: z.string(), id: z.string().regex(/^[a-f0-9]{64}$/) }).strict(),
]);

export type WorkdirOperation = z.infer<typeof operationSchema>;
export type WorkDirectory = z.infer<typeof directorySchema>;
export type WorkdirReport = z.infer<typeof reportSchema>;
export type WorkdirResponse = z.infer<typeof responseSchema>;
export type WorkdirCommand = z.infer<typeof commandSchema>;

/** Validate untrusted IPC requests without accepting filesystem paths or CLI flags. */
export function parseWorkdirCommand(input: unknown): WorkdirCommand {
  return commandSchema.parse(input);
}

/** Parse the local CLI response; malformed contracts cannot report successful deletion. */
export function parseWorkdirResponse(input: unknown): WorkdirResponse {
  const result = parseWithFallback<WorkdirResponse | null>(input, responseSchema, null, {
    endpoint: "local-daemon/workdirs",
  });
  if (result === null) throw new Error("Invalid work-directory response; update the local daemon and try again");
  return result;
}
