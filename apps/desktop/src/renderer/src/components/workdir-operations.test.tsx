import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { WorkdirOperation } from "@multica/core/api/workdirs";
import translations from "../../../../../../packages/views/locales/en/settings.json";
import { WorkdirOperations } from "./workdir-operations";

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (selector: (resource: typeof translations) => string, values?: Record<string, string | number>): string =>
      Object.entries(values ?? {}).reduce((text, [key, value]) => text.replace(`{{${key}}}`, String(value)), selector(translations)),
  }),
}));

const operation: WorkdirOperation = {
  id: "cleanup-a", profile: "desktop-local", directoryId: "directory-a",
  action: "delete", status: "succeeded", stage: "deleting",
  path: "/managed/work-a", currentPath: "", elapsedMs: 2000, stageElapsedMs: 1000,
  removedFiles: 12, error: "",
  result: { removed: 1, skipped: 0, reclaimedBytes: 2048, problems: [] },
};

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("WorkdirOperations notices", () => {
  it("archives success after five seconds without restarting the timer on polling", () => {
    const { rerender } = render(<WorkdirOperations operations={[operation]} directories={[]} />);
    act(() => vi.advanceTimersByTime(4000));
    rerender(<WorkdirOperations operations={[{ ...operation }]} directories={[]} />);
    act(() => vi.advanceTimersByTime(999));
    expect(screen.getByRole("region", { name: "Background cleanup" })).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.queryByRole("region", { name: "Background cleanup" })).not.toBeInTheDocument();
    const summary = screen.getByText("Cleanup history (1)");
    const history = summary.closest("details");
    expect(history).not.toHaveAttribute("open");
    expect(screen.getByText("/managed/work-a")).not.toBeVisible();
    fireEvent.click(summary);
    expect(history).toHaveAttribute("open");
    expect(screen.getByText("/managed/work-a")).toBeVisible();
    expect(screen.getByText("Completed")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Move to history" })).not.toBeInTheDocument();
  });

  it("keeps running work visible and gives each completion its own notice period", () => {
    const running: WorkdirOperation = { ...operation, id: "cleanup-b", status: "running", path: "/managed/work-b" };
    const { rerender } = render(<WorkdirOperations operations={[operation, running]} directories={[]} />);
    act(() => vi.advanceTimersByTime(4000));
    rerender(<WorkdirOperations operations={[operation, { ...running, status: "succeeded" }]} directories={[]} />);
    act(() => vi.advanceTimersByTime(1000));
    const notices = screen.getByRole("region", { name: "Background cleanup" });
    expect(within(notices).queryByText("/managed/work-a")).not.toBeInTheDocument();
    expect(within(notices).getByText("/managed/work-b")).toBeVisible();
    act(() => vi.advanceTimersByTime(4000));
    expect(screen.queryByRole("region", { name: "Background cleanup" })).not.toBeInTheDocument();
    expect(screen.getByText("Cleanup history (2)")).toBeInTheDocument();
  });

  it.each<WorkdirOperation>([
    { ...operation, status: "failed", error: "Remote branch unavailable" },
    { ...operation, result: { ...operation.result, skipped: 1 } },
    { ...operation, result: { ...operation.result, problems: [{ path: "/managed/work-a", reason: "Unpushed changes" }] } },
    { ...operation, error: "Cleanup requires attention" },
    { ...operation, status: "unknown" },
  ])("retains exceptional results until manually archived: $status / $error", (result) => {
    const { rerender } = render(<WorkdirOperations operations={[result]} directories={[]} />);
    act(() => vi.advanceTimersByTime(30000));
    expect(screen.getByRole("region", { name: "Background cleanup" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Move to history" }));
    rerender(<WorkdirOperations operations={[{ ...result }]} directories={[]} />);
    expect(screen.queryByRole("region", { name: "Background cleanup" })).not.toBeInTheDocument();
    expect(screen.getByText("Cleanup history (1)")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Cleanup history (1)"));
    expect(screen.getByText("/managed/work-a", { selector: "p" })).toBeVisible();
    if (result.error) expect(screen.getByRole("alert")).toHaveTextContent(result.error);
  });

  it.each<WorkdirOperation["status"]>(["queued", "running"])("never archives or offers to hide %s work", (status) => {
    render(<WorkdirOperations operations={[{ ...operation, status }]} directories={[]} />);
    act(() => vi.advanceTimersByTime(30000));
    expect(screen.getByRole("region", { name: "Background cleanup" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Move to history" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Cleanup history/)).not.toBeInTheDocument();
  });

  it("isolates identical operation IDs across daemon profiles and cancels timers on unmount", () => {
    const other: WorkdirOperation = { ...operation, profile: "desktop-other", status: "failed", path: "/other/work-a" };
    const { unmount } = render(<WorkdirOperations operations={[operation, other]} directories={[]} />);
    fireEvent.click(screen.getAllByRole("button", { name: "Move to history" })[0]);
    const notices = screen.getByRole("region", { name: "Background cleanup" });
    expect(within(notices).getByText("/managed/work-a")).toBeVisible();
    expect(within(notices).queryByText("/other/work-a")).not.toBeInTheDocument();
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
