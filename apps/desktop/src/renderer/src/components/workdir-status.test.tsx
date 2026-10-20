import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { WorkDirectory } from "@multica/core/api/workdirs";
import type { IssueStatusEntry } from "@multica/core/types";
import settings from "../../../../../../packages/views/locales/zh-Hans/settings.json";
import issues from "../../../../../../packages/views/locales/zh-Hans/issues.json";
import autopilots from "../../../../../../packages/views/locales/zh-Hans/autopilots.json";
import { WorkdirStatus } from "./workdir-status";

const mocks = vi.hoisted(() => ({ listStatuses: vi.fn() }));
vi.mock("@multica/core/api", () => ({
  api: { listIssueStatuses: mocks.listStatuses },
}));
vi.mock("@multica/views/i18n", () => ({
  useT: (namespace: string) => ({
    t: (selector: (resource: unknown) => string) => selector(namespace === "issues" ? issues : namespace === "autopilots" ? autopilots : settings),
  }),
}));

const row: WorkDirectory = {
  id: "directory", profile: "local", workspaceId: "other-workspace", workspaceName: "Team",
  workspaceSlug: "other-team", targetKind: "issue", targetId: "issue-id",
  parentId: "issue-id", parentTitle: "Task", parentStatus: "todo",
  kind: "issue", mode: "script", path: "/managed/task", sizeBytes: 1,
  active: false, blockedReason: "",
};
const customStatus: IssueStatusEntry = {
  id: "status-qa", workspace_id: row.workspaceId, key: "qa", name: "测试中",
  description: "", category: "started", color: "#a855f7", icon: "three_quarters",
  is_system: false, position: 1, archived_at: "2026-10-01", created_at: "", updated_at: "",
};
const serverUrl = "http://localhost:18740";

/** Render a real status component and query hook with isolated API data. */
function mount(overrides: Partial<WorkDirectory> = {}, ownerUrl = serverUrl) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><WorkdirStatus row={{ ...row, ...overrides }} serverUrl={ownerUrl} currentServerUrl={serverUrl} /></QueryClientProvider>);
}

beforeEach(() => {
  mocks.listStatuses.mockReset().mockResolvedValue({ statuses: [customStatus] });
});

describe("WorkdirStatus", () => {
  it.each([
    ["todo", "待办"], ["in_progress", "进行中"], ["in_review", "审核中"],
    ["done", "已完成"], ["blocked", "已阻塞"], ["cancelled", "已取消"],
  ])("translates issue status %s and includes its icon", (status, label) => {
    const view = mount({ parentStatus: status });
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(view.container.querySelector("svg")).toBeInTheDocument();
    expect(screen.queryByText(status)).not.toBeInTheDocument();
  });
  it("loads the owning workspace's archived custom status and preserves its color", async () => {
    const view = mount({ parentStatus: "qa" });
    expect(await screen.findByText("测试中")).toBeInTheDocument();
    expect(mocks.listStatuses).toHaveBeenCalledWith(true, row.workspaceId);
    expect(view.container.querySelector("svg")).toHaveStyle({ color: "#a855f7" });
  });
  it("never fetches another service's workspace catalog", async () => {
    mount({ parentStatus: "qa" }, "https://other.example");
    expect(screen.getByText("未知")).toBeInTheDocument();
    await waitFor(() => expect(mocks.listStatuses).not.toHaveBeenCalled());
    expect(screen.queryByText("测试中")).not.toBeInTheDocument();
  });
  it.each([
    ["run", "completed", "已完成"], ["run", "failed", "失败"],
    ["run", "deferred", "已延后"], ["run", "waiting_local_directory", "等待本地目录释放"],
    ["chat", "active", "活跃"], ["chat", "archived", "已归档"],
    ["autopilot_run", "skipped", "已跳过"],
  ])("renders %s/%s with a localized label and icon", (kind, status, label) => {
    const view = mount({ kind, parentStatus: status });
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(view.container.querySelector("svg")).toBeInTheDocument();
    expect(mocks.listStatuses).not.toHaveBeenCalled();
  });
  it("keeps an active directory's running indication ahead of the issue status", () => {
    mount({ parentStatus: "done", active: true });
    expect(screen.getByText(settings.desktop.workdirs.running)).toBeInTheDocument();
    expect(screen.queryByText("已完成")).not.toBeInTheDocument();
  });
  it("does not treat an unknown run state as an issue status", () => {
    mount({ kind: "run", parentStatus: "todo" });
    expect(screen.getByText("未知")).toBeInTheDocument();
    expect(screen.queryByText("待办")).not.toBeInTheDocument();
  });
  it("keeps built-in translations available when the catalog cannot load", async () => {
    mocks.listStatuses.mockRejectedValue(new Error("offline"));
    mount({ parentStatus: "in_review" });
    expect(screen.getByText("审核中")).toBeInTheDocument();
    await waitFor(() => expect(mocks.listStatuses).toHaveBeenCalled());
  });
});
