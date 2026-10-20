import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "@multica/core/api";
import { agentTaskSnapshotKeys } from "@multica/core/agents";
import { chatKeys } from "@multica/core/chat/queries";
import type { TaskMessagePayload } from "@multica/core/types/events";
import type { AgentTask } from "@multica/core/types/agent";
import { renderWithI18n } from "../test/i18n";
import { QuickCreateProgress } from "./quick-create-progress";

vi.mock("../common/task-transcript/transcript-button", () => ({ TranscriptButton: ({ title }: { title: string }) => <button>{title}</button> }));
vi.mock("@multica/core/api", () => ({ api: { getAgentTaskSnapshot: vi.fn(), listTaskMessages: vi.fn() } }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: (select: (state: unknown) => unknown) => select({ user: { id: "me" } }) }));
afterEach(() => vi.clearAllMocks());
const task: AgentTask = {
  id: "00000000-0000-0000-0000-000000000001", agent_id: "agent", runtime_id: "runtime", issue_id: "",
  kind: "quick_create", status: "dispatched", priority: 0, dispatched_at: new Date().toISOString(),
  started_at: null, completed_at: null, created_at: new Date().toISOString(), error: null, result: null,
  trigger_summary: "Build a page", attribution: { source: "quick_create", precise: true, initiator: { id: "me" } },
};
function setup(tasks = [task], messages: TaskMessagePayload[] = []) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.mocked(api.getAgentTaskSnapshot).mockResolvedValue(tasks);
  vi.mocked(api.listTaskMessages).mockResolvedValue(messages);
  client.setQueryData(agentTaskSnapshotKeys.list("ws"), tasks);
  renderWithI18n(<QueryClientProvider client={client}><QuickCreateProgress wsId="ws" /></QueryClientProvider>);
  return client;
}
describe("quick-create progress", () => {
  it("shows live script output before an issue exists and retains failure diagnostics", async () => {
    const client = setup();
    expect(screen.getByText("Preparing run")).toBeInTheDocument();
    await waitFor(() => expect(api.listTaskMessages).toHaveBeenCalledWith(task.id));
    const messages: TaskMessagePayload[] = [
      { task_id: task.id, issue_id: "", seq: 1, type: "tool_use", tool: "Workspace initialization" },
      { task_id: task.id, issue_id: "", seq: 2, type: "tool_result", tool: "Initialization output", output: "stdout: copying\nstderr: missing dependency\n" },
    ];
    vi.mocked(api.listTaskMessages).mockResolvedValue(messages);
    await waitFor(() => expect(screen.getByText("Initializing workspace")).toBeInTheDocument(), { timeout: 2500 });
    expect(screen.getByText(/stdout: copying/)).toHaveTextContent("stderr: missing dependency");
    await act(async () => {
      client.setQueryData(agentTaskSnapshotKeys.list("ws"), [{ ...task, status: "failed", error: "exit status 7" }]);
    });
    await waitFor(() => expect(screen.getByText("Creation stopped")).toBeInTheDocument());
    expect(screen.getByText("exit status 7")).toBeInTheDocument();
    expect(screen.getByText(/stdout: copying/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(screen.queryByRole("complementary")).not.toBeInTheDocument());
    client.clear();
  });
  it("keeps silent initialization visible and removes the panel on success", async () => {
    const client = setup();
    await waitFor(() => expect(api.listTaskMessages).toHaveBeenCalled());
    await act(async () => {
      client.setQueryData(chatKeys.taskMessages(task.id), [{ task_id: task.id, seq: 1, type: "tool_use", tool: "Workspace initialization" }]);
    });
    expect(screen.getByText("Initializing workspace")).toBeInTheDocument();
    expect(screen.getByText("Waiting for script output...")).toBeInTheDocument();
    await act(async () => { client.setQueryData(agentTaskSnapshotKeys.list("ws"), [{ ...task, status: "completed", issue_id: "created" }]); });
    await waitFor(() => expect(screen.queryByRole("complementary")).not.toBeInTheDocument());
    client.clear();
  });
  it("restores an in-progress script and its persisted output after remount", async () => {
    const client = setup([task], [
      { task_id: task.id, issue_id: "", seq: 1, type: "tool_use", tool: "Workspace initialization" },
      { task_id: task.id, issue_id: "", seq: 2, type: "tool_result", tool: "Initialization output", output: "Downloaded dependencies" },
    ]);
    await waitFor(() => expect(screen.getByText("Downloaded dependencies")).toBeInTheDocument());
    expect(screen.getByText("Initializing workspace")).toBeInTheDocument();
    client.clear();
  });
  it("does not open output for another member's creation or ordinary runs", () => {
    const client = setup([{ ...task, attribution: { source: "quick_create", precise: true, initiator: { id: "other" } } }, { ...task, id: "normal", kind: "direct" }]);
    expect(screen.queryByRole("complementary")).not.toBeInTheDocument();
    expect(api.listTaskMessages).not.toHaveBeenCalled();
    client.clear();
  });
});
