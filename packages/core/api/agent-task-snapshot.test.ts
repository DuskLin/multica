// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";
afterEach(() => vi.unstubAllGlobals());
describe("agent task snapshot boundary", () => {
  it("defaults missing fields for pending runs", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify([{ id: "run", kind: "quick_create" }]))));
    const tasks = await new ApiClient("https://example.test").getAgentTaskSnapshot();
    expect(tasks[0]).toMatchObject({ id: "run", kind: "quick_create", status: "cancelled", issue_id: "", dispatched_at: null });
  });
  it.each([null, {}, [{ id: 42 }]])("rejects malformed snapshot %j", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body))));
    expect(await new ApiClient("https://example.test").getAgentTaskSnapshot()).toEqual([]);
  });
});
