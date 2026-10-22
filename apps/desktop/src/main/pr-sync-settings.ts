import {
  parseLocalPRSyncCommand,
  type LocalPRSyncLive,
  type LocalPRSyncSettings,
} from "@multica/core/api/local-pr-sync";

type PRSyncContext = {
  serverUrl: string;
  configuredWorkspaceId: string;
  environmentManaged: boolean;
  running: boolean;
  external: boolean;
  activeTaskCount: number | null;
  workspaceIds: string[];
  live: LocalPRSyncLive | null;
};

type PRSyncDependencies = {
  read: () => Promise<PRSyncContext>;
  save: (workspaceId: string) => Promise<void>;
  restart: () => Promise<void>;
};

/** Derive a fail-closed control state from current profile configuration and live health. */
export function describePRSync(context: PRSyncContext, workspaceId: string): LocalPRSyncSettings {
  const live = context.live;
  const configured = context.configuredWorkspaceId;
  const environmentManaged = context.environmentManaged || live?.environmentManaged === true;
  const effective = environmentManaged && live ? live.workspaceId : configured;
  let blocked: LocalPRSyncSettings["blocked"] = "";
  if (!context.running) blocked = "offline";
  else if (context.external) blocked = "external";
  else if (!live) blocked = "unsupported";
  else if (environmentManaged) blocked = "environment";
  else if ((configured && configured !== workspaceId) || (live.workspaceId && live.workspaceId !== workspaceId)) blocked = "other_workspace";
  else if (!context.workspaceIds.includes(workspaceId)) blocked = "unregistered";
  return {
    enabled: effective === workspaceId,
    pendingRestart: Boolean(live && !environmentManaged && live.workspaceId !== configured),
    canRestart: blocked === "" && context.activeTaskCount === 0,
    phase: live?.workspaceId === workspaceId ? live.phase : "disabled",
    blocked,
  };
}

/** Validate the requested service and workspace on every read, write and restart. */
export async function runPRSyncSettings(input: unknown, dependencies: PRSyncDependencies): Promise<LocalPRSyncSettings> {
  const command = parseLocalPRSyncCommand(input);
  const read = async (): Promise<PRSyncContext> => {
    const context = await dependencies.read();
    if (new URL(context.serverUrl).href !== new URL(command.serverUrl).href) {
      throw new Error("The desktop service changed; reopen settings");
    }
    return context;
  };
  const context = await read();
  const settings = describePRSync(context, command.workspaceId);
  if (command.action === "get") return settings;
  if (settings.blocked) throw new Error(`Local PR sync is unavailable: ${settings.blocked}`);
  if (command.action === "set") {
    await dependencies.save(command.enabled ? command.workspaceId : "");
  } else {
    if (!settings.canRestart) throw new Error("Wait for active runs to finish before restarting");
    await dependencies.restart();
  }
  return describePRSync(await read(), command.workspaceId);
}
