// @vitest-environment node
import { mkdtemp, writeFile } from "fs/promises";
import { join } from "path";
import { tmpdir } from "os";
import { describe, expect, it } from "vitest";
import { loadRuntimeConfig } from "./runtime-config-loader";

describe("loadRuntimeConfig", () => {
  it("uses bundled self-host defaults and lets the user's config override them", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-bundled-config-"));
    const configPath = join(dir, "desktop.json");
    const bundledConfigPath = join(dir, "defaults.json");
    await writeFile(
      bundledConfigPath,
      JSON.stringify({
        schemaVersion: 1,
        apiUrl: "https://multica.example.com",
      }),
    );
    const options = { isDev: false, env: {}, configPath, bundledConfigPath };
    expect(await loadRuntimeConfig(options)).toMatchObject({
      ok: true,
      config: {
        apiUrl: "https://multica.example.com",
        wsUrl: "wss://multica.example.com/ws",
      },
    });
    await writeFile(
      configPath,
      JSON.stringify({
        schemaVersion: 1,
        apiUrl: "https://custom.example.com",
      }),
    );
    expect(await loadRuntimeConfig(options)).toMatchObject({
      ok: true,
      config: { apiUrl: "https://custom.example.com" },
    });
  });

  it("fails closed on malformed bundled deployment defaults", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-bundled-config-"));
    const bundledConfigPath = join(dir, "defaults.json");
    await writeFile(bundledConfigPath, "{");
    expect(
      await loadRuntimeConfig({
        isDev: false,
        env: {},
        configPath: join(dir, "missing.json"),
        bundledConfigPath,
      }),
    ).toMatchObject({ ok: false });
  });
  it("uses dev env and ignores desktop.json during electron-vite dev", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://prod.example.com" }),
    );

    await expect(
      loadRuntimeConfig({
        isDev: true,
        configPath,
        env: {
          apiUrl: "http://localhost:8080",
          wsUrl: "ws://localhost:8080/ws",
          appUrl: "http://localhost:3000",
        },
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "http://localhost:8080",
        wsUrl: "ws://localhost:8080/ws",
        appUrl: "http://localhost:3000",
      },
    });
  });

  it("uses cloud defaults when packaged config is absent", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    await expect(
      loadRuntimeConfig({
        isDev: false,
        configPath: join(dir, "missing.json"),
        env: {},
      }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://api.multica.ai",
        wsUrl: "wss://api.multica.ai/ws",
        appUrl: "https://multica.ai",
      },
    });
  });

  it("parses a valid packaged desktop.json", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(
      configPath,
      JSON.stringify({ schemaVersion: 1, apiUrl: "https://api.example.com" }),
    );

    await expect(
      loadRuntimeConfig({ isDev: false, configPath, env: {} }),
    ).resolves.toEqual({
      ok: true,
      config: {
        schemaVersion: 1,
        apiUrl: "https://api.example.com",
        wsUrl: "wss://api.example.com/ws",
        appUrl: "https://example.com",
      },
    });
  });

  it("fails closed when packaged desktop.json is invalid", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-desktop-config-"));
    const configPath = join(dir, "desktop.json");
    await writeFile(configPath, "{");

    const result = await loadRuntimeConfig({
      isDev: false,
      configPath,
      env: {},
    });

    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.error.message).toContain(configPath);
      expect(result.error.message).toContain(
        "Invalid desktop runtime config JSON",
      );
    }
  });
});
