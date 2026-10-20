// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWorkdirCommand, parseWorkdirResponse } from "./workdirs";

const response = {
  reports: [{ profile: "desktop-local", root: "/managed", directories: [{
    id: "directory-id", profile: "desktop-local", workspace_id: "workspace-a",
    path: "/managed/task-a", size_bytes: 100,
  }] }],
  result: { removed: 0, skipped: 0, reclaimed_bytes: 0 },
};

describe("work-directory response boundary", () => {
  it("defaults absent status and capabilities to unknown and offline", () => {
    const parsed = parseWorkdirResponse(response);
    expect(parsed.reports[0]?.online).toBe(false);
    expect(parsed.reports[0]?.serverUrl).toBe("");
    expect(parsed.reports[0]?.directories[0]).toMatchObject({ workspaceId: "workspace-a", parentStatus: "", active: false, workspaceSlug: "", targetKind: "", targetId: "" });
  });
  it("rejects malformed inventory and success counts", () => {
    expect(() => parseWorkdirResponse({ ...response, reports: null })).toThrow();
    expect(() => parseWorkdirResponse({ ...response, result: { removed: -1, skipped: 0, reclaimed_bytes: 0 } })).toThrow();
  });
  it("rejects paths and extra destructive options at the IPC boundary", () => {
    expect(() => parseWorkdirCommand({ action: "delete", profile: "", id: "/outside" })).toThrow();
    expect(() => parseWorkdirCommand({ action: "delete", profile: "", id: "a".repeat(64), force: true })).toThrow();
    expect(parseWorkdirCommand({ action: "delete", profile: "", id: "a".repeat(64) })).toEqual({ action: "delete", profile: "", id: "a".repeat(64) });
    expect(parseWorkdirCommand({ action: "start-delete", profile: "", id: "a".repeat(64) }).action).toBe("start-delete");
    expect(() => parseWorkdirCommand({ action: "start-delete", profile: "", id: "/tmp/user-data" })).toThrow();
    expect(() => parseWorkdirCommand({ action: "operations", root: "/outside" })).toThrow();
  });
  it("parses progress without treating unknown states or malformed counters as completion", () => {
    const job = {
      id: "job", profile: "desktop-local", directory_id: "a".repeat(64), action: "delete",
      status: "running", stage: "remote", elapsed_ms: 2000, stage_elapsed_ms: 1000,
      removed_files: 0, result: response.result,
    };
    expect(parseWorkdirResponse({ ...response, operations: [job] }).operations[0]).toMatchObject({
      stage: "remote", elapsedMs: 2000, status: "running",
    });
    expect(parseWorkdirResponse({ ...response, operations: [{ ...job, status: "future", stage: "future" }] }).operations[0]).toMatchObject({ status: "unknown", stage: "unknown" });
    expect(() => parseWorkdirResponse({ ...response, operations: [{ ...job, removed_files: -1 }] })).toThrow();
    expect(() => parseWorkdirResponse({ ...response, operations: [{ ...job, elapsed_ms: "slow" }] })).toThrow();
  });
});
