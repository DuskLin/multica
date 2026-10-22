import { z } from "zod";
import { parseWithFallback } from "./schema";

const phaseSchema = z.enum(["disabled", "waiting", "syncing", "idle", "error", "unknown"]);
const liveSchema = z.object({
  workspace_id: z.union([z.uuid(), z.literal("")]),
  phase: phaseSchema.catch("unknown"),
  environment_managed: z.boolean(),
}).transform((value) => ({
  workspaceId: value.workspace_id,
  phase: value.phase,
  environmentManaged: value.environment_managed,
}));
export type LocalPRSyncLive = z.infer<typeof liveSchema>;

/** Parse additive daemon diagnostics; older or malformed daemons remain unsupported. */
export function parseLocalPRSyncLive(value: unknown): LocalPRSyncLive | null {
  return parseWithFallback<LocalPRSyncLive | null>(value, liveSchema, null, { endpoint: "daemon/health/pr_sync" });
}

const targetSchema = z.object({ serverUrl: z.url(), workspaceId: z.uuid() });
const commandSchema = z.discriminatedUnion("action", [
  targetSchema.extend({ action: z.literal("get") }),
  targetSchema.extend({ action: z.literal("set"), enabled: z.boolean() }),
  targetSchema.extend({ action: z.literal("restart") }),
]);
export type LocalPRSyncCommand = z.infer<typeof commandSchema>;

/** Validate IPC input before selecting a profile or writing settings. */
export function parseLocalPRSyncCommand(input: unknown): LocalPRSyncCommand {
  return commandSchema.parse(input);
}

const responseSchema = z.object({
  enabled: z.boolean(),
  pendingRestart: z.boolean(),
  canRestart: z.boolean(),
  phase: phaseSchema,
  blocked: z.enum(["", "offline", "unsupported", "external", "environment", "other_workspace", "unregistered"]),
});
export type LocalPRSyncSettings = z.infer<typeof responseSchema>;

/** Reject malformed IPC replies instead of displaying an unverified toggle state. */
export function parseLocalPRSyncSettings(input: unknown): LocalPRSyncSettings {
  return responseSchema.parse(input);
}
