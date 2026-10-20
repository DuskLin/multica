import type { WorkDirectory } from "@multica/core/api/workdirs";
import { paths } from "@multica/core/paths";

type WorkdirLink =
  | { status: "ready"; href: string }
  | { status: "other_server" | "unavailable" };

/**
 * Normalize an API location without conflating different ports or base paths.
 * @param value Configured API address.
 * @returns A comparable HTTP location, or null for missing/invalid addresses.
 */
function serverLocation(value: string): string | null {
  try {
    const url = new URL(value);
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash) return null;
    return `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
  } catch {
    return null;
  }
}

/**
 * Resolve verified issue/chat destinations only within the connected service.
 * @param row Directory metadata with an authenticated target.
 * @param serverUrl Service that owns the directory's task.
 * @param currentServerUrl Desktop's connected API location.
 * @returns A centralized application route or an explicit unavailable state.
 */
export function resolveWorkdirLink(row: WorkDirectory, serverUrl: string, currentServerUrl: string): WorkdirLink {
  const owner = serverLocation(serverUrl);
  const current = serverLocation(currentServerUrl);
  if (owner === null || current === null) return { status: "unavailable" };
  if (owner !== current) return { status: "other_server" };
  if (!row.workspaceSlug || !row.targetId) return { status: "unavailable" };
  const workspace = paths.workspace(row.workspaceSlug);
  if (row.targetKind === "issue") return { status: "ready", href: workspace.issueDetail(row.targetId) };
  if (row.targetKind === "chat") return { status: "ready", href: workspace.chatSession(row.targetId) };
  return { status: "unavailable" };
}
