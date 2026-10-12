// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { configStore } from "../config";
import { ApiClient } from "./client";
import { AppConfigSchema } from "./schemas";

const resource = {
  id: "r1", project_id: "p1", workspace_id: "w1", resource_type: "local_directory",
  resource_ref: { local_path: "/source", daemon_id: "d1", execution_mode: "script", init_script: "init.sh" },
};

afterEach(() => {
  vi.unstubAllGlobals();
  configStore.getState().setLocalScriptSupported(false);
});

function respond(body: unknown) {
  vi.stubGlobal("fetch", vi.fn().mockImplementation(async () => new Response(JSON.stringify(body), { status: 200 })));
  return new ApiClient("https://api.example.test");
}

describe("project resource API compatibility", () => {
  it("keeps script configuration and defaults optional response fields", async () => {
    const client = respond({ resources: [resource], total: 1 });
    const result = await client.listProjectResources("p1");
    expect(result.resources[0]).toMatchObject({ ...resource, label: null, position: 0 });
  });

  it.each([42, null, []])("rejects a malformed script path %j", async (initScript) => {
    const client = respond({ resources: [{ ...resource, resource_ref: { ...resource.resource_ref, init_script: initScript } }], total: 1 });
    expect(await client.listProjectResources("p1")).toEqual({ resources: [], total: 0 });
  });

  it("accepts older refs and preserves unknown modes for the UI fallback", async () => {
    const refs = [{ local_path: "/source", daemon_id: "d1" }, { ...resource.resource_ref, execution_mode: "future" }];
    const client = respond({ resources: refs.map((ref) => ({ ...resource, resource_ref: ref })), total: 2 });
    const result = await client.listProjectResources("p1");
    expect(result.resources.map((row) => row.resource_ref)).toEqual(refs);
  });

  it("validates both mutation responses", async () => {
    configStore.getState().setLocalScriptSupported(true);
    const client = respond({ ...resource, resource_ref: { init_script: 3 } });
    const data = { resource_type: "local_directory" as const, resource_ref: resource.resource_ref };
    expect((await client.createProjectResource("p1", data)).id).toBe("");
    expect((await client.updateProjectResource("p1", "r1", data)).id).toBe("");
  });

  it.each([undefined, "yes", null, false, true])("only trusts an explicit script capability %j", (flag) => {
    expect(AppConfigSchema.parse({ local_script_supported: flag }).local_script_supported).toBe(flag === true);
  });
});

it("blocks script writes before an old server can drop the isolation fields", async () => {
  const client = respond(resource);
  const data = { resource_type: "local_directory" as const, resource_ref: resource.resource_ref };
  await expect(client.createProjectResource("p1", data)).rejects.toThrow(/does not support script isolation/);
  await expect(client.updateProjectResource("p1", "r1", data)).rejects.toThrow(/does not support script isolation/);
  await expect(client.createProject({ title: "Script project", resources: [data] })).rejects.toThrow(/does not support script isolation/);
  expect(fetch).not.toHaveBeenCalled();
});
