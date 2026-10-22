import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { LocalPRSyncSettings, LocalPRSyncCommand } from "@multica/core/api/local-pr-sync";
import translations from "../../../../../../packages/views/locales/zh-Hans/settings.json";

const mocks = vi.hoisted(() => ({ command: vi.fn(), workspaceId: "01900000-0000-7000-8000-000000000001", openLog: vi.fn() }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => mocks.workspaceId }));
vi.mock("@multica/views/i18n", () => ({ useT: () => ({ t: (selector: (value: typeof translations) => string) => selector(translations) }) }));
vi.mock("@multica/views/settings", () => ({
  SettingsCard: ({ children }: { children: ReactNode }) => <section>{children}</section>,
  SettingsRow: ({ children, label, description }: { children: ReactNode; label: string; description: string }) => <div>{label}<p>{description}</p>{children}</div>,
}));
import { LocalPRSyncSettings as Settings } from "./local-pr-sync-settings";

const disabled: LocalPRSyncSettings = { enabled: false, pendingRestart: false, canRestart: true, phase: "disabled", blocked: "" };

/** Give every settings instance an isolated query cache. */
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><Settings /></QueryClientProvider>);
}

beforeEach(() => {
  mocks.command.mockReset().mockResolvedValue(disabled);
  mocks.openLog.mockReset().mockResolvedValue({ success: true });
  Object.defineProperty(window, "daemonAPI", { configurable: true, value: { prSync: mocks.command, openLogFile: mocks.openLog } });
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { runtimeConfig: { ok: true, config: { apiUrl: "https://service.example" } } } });
});

describe("local PR sync settings", () => {
  it("waits for save before enabling and shows the separate restart action", async () => {
    let current = disabled;
    let save: (data: LocalPRSyncSettings) => void = () => {};
    mocks.command.mockImplementation((command: LocalPRSyncCommand) => {
      if (command.action !== "set") return Promise.resolve(current);
      return new Promise<LocalPRSyncSettings>((resolve) => { save = (data) => { current = data; resolve(data); }; });
    });
    mount();
    const toggle = await screen.findByRole("switch", { name: translations.desktop.pr_sync.title });
    await waitFor(() => expect(toggle).not.toHaveAttribute("aria-disabled", "true"));
    fireEvent.click(toggle);
    await waitFor(() => expect(toggle).toHaveAttribute("aria-disabled", "true"));
    expect(toggle).not.toBeChecked();
    save({ ...disabled, enabled: true, pendingRestart: true });
    expect(await screen.findByText(translations.desktop.pr_sync.pending)).toBeVisible();
    expect(toggle).toBeChecked();
    expect(mocks.command).toHaveBeenCalledWith({ action: "set", serverUrl: "https://service.example", workspaceId: mocks.workspaceId, enabled: true });
  });
  it("keeps the toggle unchanged when saving fails", async () => {
    mocks.command.mockImplementation((command: LocalPRSyncCommand) => command.action === "set" ? Promise.reject(new Error("disk unavailable")) : Promise.resolve(disabled));
    mount();
    const toggle = await screen.findByRole("switch");
    await waitFor(() => expect(toggle).not.toHaveAttribute("aria-disabled", "true"));
    fireEvent.click(toggle);
    expect(await screen.findByRole("alert")).toHaveTextContent("disk unavailable");
    expect(toggle).not.toBeChecked();
  });
  it("disables restart for active runs and exposes sync failure logs", async () => {
    mocks.command.mockResolvedValue({ ...disabled, enabled: true, pendingRestart: true, canRestart: false, phase: "error" });
    mount();
    expect(await screen.findByRole("button", { name: translations.desktop.pr_sync.restart })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: translations.desktop.daemon.view_logs }));
    expect(mocks.openLog).toHaveBeenCalledOnce();
  });
  it("shows why an environment-controlled switch cannot be changed", async () => {
    mocks.command.mockResolvedValue({ ...disabled, enabled: true, blocked: "environment" });
    mount();
    expect(await screen.findByText(translations.desktop.pr_sync.blocked.environment)).toBeVisible();
    expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
  });
});
