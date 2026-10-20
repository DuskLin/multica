// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

afterEach(() => {
  setCurrentWorkspace(null, null);
  vi.unstubAllGlobals();
});

it("reads another workspace's status catalog without inheriting or mutating the active workspace", async () => {
  setCurrentWorkspace("current-team", "current-id");
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ statuses: [] }), {
    status: 200, headers: { "Content-Type": "application/json" },
  }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new ApiClient("https://api.example.test");
  await client.listIssueStatuses(true, "other-id");
  const explicitHeaders = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
  expect(explicitHeaders.get("X-Workspace-Slug")).toBe("");
  expect(explicitHeaders.get("X-Workspace-ID")).toBe("other-id");
  fetchMock.mockResolvedValue(new Response(JSON.stringify({ statuses: [] }), {
    status: 200, headers: { "Content-Type": "application/json" },
  }));
  await client.listIssueStatuses();
  const defaultHeaders = new Headers(fetchMock.mock.calls[1]?.[1]?.headers);
  expect(defaultHeaders.get("X-Workspace-Slug")).toBe("current-team");
  expect(defaultHeaders.has("X-Workspace-ID")).toBe(false);
});
