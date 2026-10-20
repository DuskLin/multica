// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseWorkdirResponse } from "@multica/core/api/workdirs";
import { resolveWorkdirLink } from "./workdir-navigation";

/** Parse a wire-format fixture through the same boundary used by desktop IPC. */
function directory(targetKind = "issue", kind = "issue") {
  const response = parseWorkdirResponse({
    reports: [{ profile: "local", root: "/managed", directories: [{
      id: "directory", profile: "local", workspace_id: "ws",
      workspace_slug: "other team", parent_id: "run-id", kind,
      target_kind: targetKind, target_id: "task/id", path: "/managed/task", size_bytes: 1,
    }] }],
    result: { removed: 0, skipped: 0, reclaimed_bytes: 0 },
  });
  const row = response.reports[0]?.directories[0];
  if (!row) throw new Error("Missing test directory");
  return row;
}

describe("work-directory destinations", () => {
  it("uses centralized issue and chat routes with encoded identifiers", () => {
    expect(resolveWorkdirLink(directory(), "https://api.example/", "https://api.example")).toEqual({
      status: "ready", href: "/other%20team/issues/task%2Fid",
    });
    expect(resolveWorkdirLink(directory("chat", "chat"), "https://api.example", "https://api.example")).toEqual({
      status: "ready", href: "/other%20team/chat?session=task%2Fid",
    });
  });
  it("opens the verified parent of a run rather than treating the run ID as an issue", () => {
    expect(resolveWorkdirLink(directory("issue", "run"), "https://api.example", "https://api.example")).toEqual({
      status: "ready", href: "/other%20team/issues/task%2Fid",
    });
  });
  it.each(["https://other.example", "https://api.example:8443", "https://api.example/other"])("rejects another service: %s", (server) => {
    expect(resolveWorkdirLink(directory(), server, "https://api.example")).toEqual({ status: "other_server" });
  });
  it.each(["", "file:///tmp/server", "invalid", "https://user:secret@api.example"])("rejects unknown or unsafe service identity: %s", (server) => {
    expect(resolveWorkdirLink(directory(), server, "https://api.example")).toEqual({ status: "unavailable" });
  });
  it("does not invent destinations for missing metadata or unknown target types", () => {
    expect(resolveWorkdirLink({ ...directory(), targetId: "" }, "https://api.example", "https://api.example")).toEqual({ status: "unavailable" });
    expect(resolveWorkdirLink({ ...directory(), workspaceSlug: "" }, "https://api.example", "https://api.example")).toEqual({ status: "unavailable" });
    expect(resolveWorkdirLink(directory("future-kind"), "https://api.example", "https://api.example")).toEqual({ status: "unavailable" });
  });
});
