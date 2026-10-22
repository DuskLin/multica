// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseLocalPRSyncCommand, parseLocalPRSyncLive, parseLocalPRSyncSettings } from "./local-pr-sync";

describe("local PR sync contracts", () => {
  it("does not claim support for old or malformed daemons", () => {
    for (const value of [undefined, null, {}, { workspace_id: "", phase: "idle" }]) expect(parseLocalPRSyncLive(value)).toBeNull();
  });
  it("renders a future phase as unknown", () => {
    expect(parseLocalPRSyncLive({ workspace_id: "", phase: "new-phase", environment_managed: false })?.phase).toBe("unknown");
  });
  it("rejects unscoped writes and malformed replies", () => {
    expect(() => parseLocalPRSyncCommand({ action: "set", serverUrl: "https://service.example", workspaceId: "codedance", enabled: true })).toThrow();
    expect(() => parseLocalPRSyncSettings({ enabled: "true" })).toThrow();
  });
});
