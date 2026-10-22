// @vitest-environment node
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { ProfileConfigWriter } from "./profile-config-writer";

describe("profile configuration writes", () => {
  it("preserves concurrent credential, sync, and unknown-field changes", async () => {
    const folder = await mkdtemp(join(tmpdir(), "multica-config-"));
    try {
      const path = join(folder, "config.json");
      await writeFile(path, JSON.stringify({ token: "fake-old", unknown_setting: true }));
      const writer = new ProfileConfigWriter();
      await Promise.all([
        writer.update(path, (config) => { config.pr_sync_workspace_id = "test-workspace"; }),
        writer.update(path, (config) => { config.token = "fake-new"; }),
      ]);
      expect(JSON.parse(await readFile(path, "utf-8"))).toEqual({ token: "fake-new", pr_sync_workspace_id: "test-workspace", unknown_setting: true });
      await writer.update(path, (config) => { delete config.token; });
      expect(JSON.parse(await readFile(path, "utf-8"))).toEqual({ pr_sync_workspace_id: "test-workspace", unknown_setting: true });
      await writeFile(path, "invalid-json");
      await expect(writer.update(path, () => {})).rejects.toThrow();
      expect(await readFile(path, "utf-8")).toBe("invalid-json");
      await writeFile(path, "{}");
      await expect(writer.update(path, (config) => { config.pr_sync_workspace_id = ""; })).resolves.toBeUndefined();
    } finally { await rm(folder, { recursive: true, force: true }); }
  });
});
