import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NavigationProvider } from "@multica/views/navigation";
import type { ReactNode } from "react";
import type { WorkdirCommand, WorkdirResponse, WorkdirOperation } from "@multica/core/api/workdirs";
import translations from "../../../../../../packages/views/locales/en/settings.json";
import issueTranslations from "../../../../../../packages/views/locales/en/issues.json";
import autopilotTranslations from "../../../../../../packages/views/locales/en/autopilots.json";

const mocks = vi.hoisted(() => ({ command: vi.fn(), push: vi.fn() }));
vi.mock("@multica/views/i18n", () => ({
  useT: (namespace: string) => ({ t: (selector: (resource: unknown) => string, values?: Record<string, string | number>) =>
    Object.entries(values ?? {}).reduce((text, [key, value]) => text.replace(`{{${key}}}`, String(value)), selector(namespace === "issues" ? issueTranslations : namespace === "autopilots" ? autopilotTranslations : translations)),
  }),
}));
vi.mock("@multica/core/api", () => ({
  api: { listIssueStatuses: vi.fn().mockResolvedValue({ statuses: [] }) },
}));
vi.mock("@multica/views/settings", () => ({
  SettingsTab: ({ children, title }: { children: ReactNode; title: string }) => <section aria-label={title}>{children}</section>,
}));
import { WorkDirectoriesTab } from "./work-directories-tab";

const inventory: WorkdirResponse = {
  operations: [],
  reports: [{ profile: "desktop-local", root: "/managed", serverUrl: "http://localhost:18740", online: true, problems: [], directories: [{
    id: "a".repeat(64), profile: "desktop-local", workspaceId: "workspace-a", workspaceName: "Project team", workspaceSlug: "other-team", targetKind: "issue", targetId: "issue-a",
    parentId: "issue-a", parentTitle: "Restore branch", parentStatus: "in_progress", kind: "issue", mode: "script",
    path: "/managed/work-a", sizeBytes: 2048, active: false, blockedReason: "",
  }] }], result: { removed: 0, skipped: 0, reclaimedBytes: 0, problems: [] },
};

/** Render with an isolated server-data cache for each test. */
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><NavigationProvider value={{ push: mocks.push, replace: vi.fn(), back: vi.fn(), pathname: "/current/settings", searchParams: new URLSearchParams(), hash: "", getShareableUrl: (path: string) => path }}><WorkDirectoriesTab /></NavigationProvider></QueryClientProvider>);
}

beforeEach(() => {
  mocks.push.mockReset();
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { runtimeConfig: { ok: true, config: { apiUrl: "http://localhost:18740" } } } });
  mocks.command.mockReset().mockResolvedValue(inventory);
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: { workdirs: mocks.command } });
});

describe("WorkDirectoriesTab", () => {
  it("shows workspace/task/status and refreshes only when requested", async () => {
    mount();
    expect(await screen.findByText("Restore branch")).toBeInTheDocument();
    expect(screen.getByText("Project team")).toBeInTheDocument();
    expect(screen.getByText(issueTranslations.status.in_progress)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(mocks.command).toHaveBeenCalledWith({ action: "list" }));
  });
  it("opens a task in its owning workspace through application navigation", async () => {
    mount();
    const link = await screen.findByRole("link", { name: /Restore branch/ });
    expect(link).toHaveAttribute("href", "/other-team/issues/issue-a");
    fireEvent.click(link);
    expect(mocks.push).toHaveBeenCalledWith("/other-team/issues/issue-a");
  });
  it("explains why a task from another server cannot open here", async () => {
    mocks.command.mockResolvedValue({ ...inventory, reports: inventory.reports.map((report) => ({ ...report, serverUrl: "https://other.example" })) });
    mount();
    expect(await screen.findByText(translations.desktop.workdirs.other_server)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
  it("shows unavailable task information without a misleading link", async () => {
    mocks.command.mockResolvedValue({ ...inventory, reports: inventory.reports.map((report) => ({ ...report, directories: report.directories.map((row) => ({ ...row, targetId: "" })) })) });
    mount();
    expect(await screen.findByText(translations.desktop.workdirs.link_unavailable)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
  it("requires confirmation and preserves the dialog when protection rejects deletion", async () => {
    mocks.command.mockImplementation(async (command: WorkdirCommand) => {
      if (command.action === "start-delete") throw new Error("Unpushed work must be preserved");
      return inventory;
    });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Delete /managed/work-a" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(mocks.command).not.toHaveBeenCalledWith(expect.objectContaining({ action: "start-delete" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Unpushed work");
    expect(screen.getByText("Restore branch")).toBeInTheDocument();
  });
  it("closes after acceptance while keeping the directory until deletion finishes", async () => {
    let pending: WorkdirOperation = {
      id: "job-a", profile: "desktop-local", directoryId: "a".repeat(64), action: "delete",
      status: "running", stage: "remote", path: "/managed/work-a", currentPath: "/managed/work-a/repo",
      elapsedMs: 2500, stageElapsedMs: 1500, removedFiles: 0,
      result: inventory.result, error: "",
    };
    let accepted = false;
    mocks.command.mockImplementation(async (command: WorkdirCommand) => {
      if (command.action === "start-delete") accepted = true;
      if (command.action === "start-delete" || command.action === "operations") return { ...inventory, operations: accepted ? [pending] : [] };
      return inventory;
    });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Delete /managed/work-a" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(mocks.command).toHaveBeenCalledWith({ action: "start-delete", profile: "desktop-local", id: "a".repeat(64) });
    expect(await screen.findByText("Verifying remote branches")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Restore branch/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Delete /managed/work-a" })).toBeDisabled();
    pending = { ...pending, status: "succeeded", result: { removed: 1, skipped: 0, reclaimedBytes: 2048, problems: [] } };
    mocks.command.mockImplementation(async (command: WorkdirCommand) => command.action === "operations"
      ? { ...inventory, operations: [{ ...pending }] }
      : { ...inventory, reports: [] });
    expect(await screen.findByText("Completed", {}, { timeout: 4000 })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("link", { name: /Restore branch/ })).not.toBeInTheDocument());
  });
  it("does not enable deletion or cleanup for an offline daemon", async () => {
    mocks.command.mockResolvedValue({ ...inventory, reports: inventory.reports.map((report) => ({ ...report, online: false })) });
    mount();
    expect(await screen.findByRole("button", { name: "Delete /managed/work-a" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reclaim now" })).toBeDisabled();
  });
  it("recovers running jobs on reentry and keeps other directories actionable", async () => {
    const running: WorkdirOperation = {
      id: "job-existing", profile: "desktop-local", directoryId: "b".repeat(64), action: "delete",
      status: "running", stage: "deleting", path: "/managed/another", currentPath: "/managed/another/repo",
      elapsedMs: 6000, stageElapsedMs: 4000, removedFiles: 230,
      result: inventory.result, error: "",
    };
    mocks.command.mockImplementation(async (command: WorkdirCommand) => command.action === "operations"
      ? { ...inventory, operations: [running] } : inventory);
    const page = mount();
    expect(await screen.findByText("Deleting files")).toBeInTheDocument();
    page.unmount();
    mount();
    expect(await screen.findByText("230 files or links removed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete /managed/work-a" })).toBeEnabled();
    fireEvent.click(screen.getByRole("link", { name: /Restore branch/ }));
    expect(mocks.push).toHaveBeenCalledWith("/other-team/issues/issue-a");
  });
  it("shows background cleanup results and skip reasons", async () => {
    const completed: WorkdirOperation = {
      id: "job-reclaim", profile: "desktop-local", directoryId: "", action: "reclaim", status: "succeeded",
      stage: "checking", path: "", currentPath: "/dirty", elapsedMs: 3000, stageElapsedMs: 1000, removedFiles: 10,
      result: { removed: 1, skipped: 1, reclaimedBytes: 2048, problems: [{ path: "/dirty", reason: "Unpublished work" }] }, error: "",
    };
    mocks.command.mockImplementation(async (command: WorkdirCommand) => command.action === "start-reclaim" || command.action === "operations"
      ? { ...inventory, operations: [completed] } : inventory);
    mount();
    await screen.findByText("Restore branch");
    fireEvent.click(screen.getByRole("button", { name: "Reclaim now" }));
    expect(await screen.findByText(/Removed 1 directories; skipped 1/)).toBeInTheDocument();
    expect(await screen.findByText(/Unpublished work/)).toBeInTheDocument();
  });
});
