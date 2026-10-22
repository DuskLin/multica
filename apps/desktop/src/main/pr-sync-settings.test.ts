// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import { describePRSync, runPRSyncSettings } from "./pr-sync-settings";

const workspaceId = "01900000-0000-7000-8000-000000000001";
const otherWorkspaceId = "01900000-0000-7000-8000-000000000002";
const serverUrl = "https://service.example";

/** Model a running daemon with configuration independent of its effective state. */
function fixture() {
  const context: Parameters<typeof describePRSync>[0] = {
    serverUrl, configuredWorkspaceId: "", running: true, external: false,
    activeTaskCount: 0, environmentManaged: false, workspaceIds: [workspaceId],
    live: { workspaceId: "", phase: "disabled", environmentManaged: false },
  };
  const dependencies = {
    read: vi.fn(async () => context),
    save: vi.fn(async (id: string) => { context.configuredWorkspaceId = id; }),
    restart: vi.fn(async () => { context.live = { workspaceId: context.configuredWorkspaceId, phase: "waiting", environmentManaged: false }; }),
  };
  return { context, dependencies };
}

describe("local PR sync settings", () => {
  it("saves independently of restart and reports effective state afterward", async () => {
    const { dependencies } = fixture();
    const saved = await runPRSyncSettings({ action: "set", serverUrl, workspaceId, enabled: true }, dependencies);
    expect(saved).toMatchObject({ enabled: true, pendingRestart: true, phase: "disabled" });
    expect(dependencies.restart).not.toHaveBeenCalled();
    expect(await runPRSyncSettings({ action: "restart", serverUrl, workspaceId }, dependencies)).toMatchObject({ enabled: true, pendingRestart: false, phase: "waiting" });
  });
  it("keeps the live enabled state visible after saving disable", async () => {
    const { context, dependencies } = fixture();
    context.configuredWorkspaceId = workspaceId;
    context.live = { workspaceId: workspaceId, phase: "idle", environmentManaged: false };
    expect(await runPRSyncSettings({ action: "set", serverUrl, workspaceId, enabled: false }, dependencies)).toMatchObject({ enabled: false, pendingRestart: true, phase: "idle" });
  });
  it("refuses busy or unknown-activity restarts without changing configuration", async () => {
    for (const count of [1, null]) {
      const { context, dependencies } = fixture();
      context.activeTaskCount = count;
      await expect(runPRSyncSettings({ action: "restart", serverUrl, workspaceId }, dependencies)).rejects.toThrow("active runs");
      expect(dependencies.restart).not.toHaveBeenCalled();
      expect(dependencies.save).not.toHaveBeenCalled();
    }
  });
  it("refuses other services before touching configuration", async () => {
    const { dependencies } = fixture();
    await expect(runPRSyncSettings({ action: "set", serverUrl: "https://other.example", workspaceId, enabled: true }, dependencies)).rejects.toThrow("service changed");
    expect(dependencies.save).not.toHaveBeenCalled();
  });
  it("does not move another workspace's configured or still-running sync", async () => {
    const { context, dependencies } = fixture();
    context.configuredWorkspaceId = otherWorkspaceId;
    await expect(runPRSyncSettings({ action: "set", serverUrl, workspaceId, enabled: true }, dependencies)).rejects.toThrow("other_workspace");
    context.configuredWorkspaceId = "";
    context.live = { workspaceId: otherWorkspaceId, phase: "idle", environmentManaged: false };
    await expect(runPRSyncSettings({ action: "set", serverUrl, workspaceId, enabled: false }, dependencies)).rejects.toThrow("other_workspace");
    expect(dependencies.save).not.toHaveBeenCalled();
  });
  it("locks environment overrides, external, offline, unregistered and old daemons", async () => {
    const scenarios: Array<(context: Parameters<typeof describePRSync>[0]) => void> = [
      (context) => { context.environmentManaged = true; },
      (context) => { context.live = { workspaceId: workspaceId, phase: "idle", environmentManaged: true }; },
      (context) => { context.external = true; },
      (context) => { context.running = false; },
      (context) => { context.workspaceIds = []; },
      (context) => { context.live = null; },
    ];
    for (const change of scenarios) {
      const { context, dependencies } = fixture();
      change(context);
      await expect(runPRSyncSettings({ action: "set", serverUrl, workspaceId, enabled: true }, dependencies)).rejects.toThrow("unavailable");
      expect(dependencies.save).not.toHaveBeenCalled();
    }
  });
  it("surfaces write failures and leaves the old state untouched", async () => {
    const { dependencies } = fixture();
    dependencies.save.mockRejectedValue(new Error("read-only disk"));
    await expect(runPRSyncSettings({ action: "set", serverUrl, workspaceId, enabled: true }, dependencies)).rejects.toThrow("read-only disk");
    expect(await runPRSyncSettings({ action: "get", serverUrl, workspaceId }, dependencies)).toMatchObject({ enabled: false });
  });
});
