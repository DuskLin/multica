import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

/** Serialize read-modify-write operations so auth refreshes preserve local settings. */
export class ProfileConfigWriter {
  private pending: Promise<void> = Promise.resolve();

  /** Update only the selected profile's fields; reject malformed files without replacing them. */
  update(path: string, mutate: (config: Record<string, unknown>) => void): Promise<void> {
    const operation = this.pending.catch(() => {}).then(async () => {
      const raw = await readFile(path, "utf-8").catch((error: unknown) => {
        if (error && typeof error === "object" && "code" in error && error.code === "ENOENT") return "{}";
        throw error;
      });
      const parsed: unknown = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("Invalid daemon configuration");
      const config: Record<string, unknown> = Object.fromEntries(Object.entries(parsed));
      mutate(config);
      await mkdir(dirname(path), { recursive: true });
      await writeFile(path, JSON.stringify(config, null, 2), { encoding: "utf-8", mode: 0o600 });
    });
    this.pending = operation.catch(() => {});
    return operation;
  }
}
