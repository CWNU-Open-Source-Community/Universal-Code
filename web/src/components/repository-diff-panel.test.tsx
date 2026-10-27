import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { RepositoryDiffPanel } from "./repository-diff-panel";

describe("RepositoryDiffPanel", () => {
  it("pages without accumulating patches, retries failures, and resets when the workspace changes", async () => {
    const user = userEvent.setup();
    const first = { available: true, base_head: "head", returned_count: 1, total_patch_bytes: 10,
      next_after: "a.txt", omitted_count: 1, redaction_count: 0,
      items: [{ path: "a.txt", content_state: "text", patch: "+first", added_lines: 1,
        deleted_lines: 0, statistics_complete: true }] };
    const second = { ...first, next_after: "", omitted_count: 0,
      items: [{ ...first.items[0], path: "b.txt", patch: "+last" }] };
    const repositoryDiff = vi.fn().mockResolvedValueOnce(first).mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(first).mockResolvedValueOnce(second).mockResolvedValue(first);
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    const view = render(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="first-workspace" />
    </QueryClientProvider>);
    await screen.findByText("a.txt");
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByText(/offline/);
    await user.click(screen.getByRole("button", { name: "Previous page" }));
    await screen.findByText("a.txt");
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByText("b.txt");
    expect(screen.queryByText("a.txt")).not.toBeInTheDocument();
    expect(repositoryDiff).toHaveBeenCalledWith("first-workspace", expect.any(AbortSignal), "a.txt");
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();
    view.rerender(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="second-workspace" />
    </QueryClientProvider>);
    await screen.findByText("a.txt");
    expect(repositoryDiff).toHaveBeenLastCalledWith("second-workspace", expect.any(AbortSignal), "");
  });

  it("distinguishes full counts on a truncated preview from unavailable counts", async () => {
    const client = { repositoryDiff: vi.fn().mockResolvedValue({ available: true, base_head: "head",
      returned_count: 2, total_patch_bytes: 100, omitted_count: 0, redaction_count: 0,
      items: [{ path: "big.txt", content_state: "text", patch: "-old\n", added_lines: 20000,
        deleted_lines: 20000, statistics_complete: true, truncated: true },
      { path: "complex.txt", content_state: "computation_limited", patch: "", added_lines: 0,
        deleted_lines: 0, statistics_complete: false }] }) } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient()}>
      <RepositoryDiffPanel client={client} workspaceID="stats" />
    </QueryClientProvider>);
    expect(await screen.findByText("20000 added / 20000 deleted")).toBeVisible();
    expect(screen.getByText(/counts above include all changes/)).toBeVisible();
    expect(screen.getByText("Line counts unavailable")).toBeVisible();
    expect(screen.queryByText("0 added / 0 deleted")).not.toBeInTheDocument();
  });
  it("renders only the bounded redacted patch projection", async () => {
    const repositoryDiff = vi.fn().mockResolvedValue({
      protocol_version: "repository_diff.v1", workspace_id: "workspace-1",
      kind: "git", available: true, base_head: "1234567890ab",
      items: [{ path: "src/main.go", staging: "unmodified", worktree: "modified",
        content_state: "text", patch: "+token=[REDACTED:api-key]\n", patch_bytes: 31,
        added_lines: 1, deleted_lines: 0, redacted: true, truncated: false }],
      returned_count: 1, omitted_count: 0, redaction_count: 1, total_patch_bytes: 31,
      truncated: false, read_only: true, instruction_authorized: false,
      mutation_supported: false, authority_granted: false, root_path_exposed: false,
      raw_content_included: false, patch_content_included: true,
      remote_config_included: false, process_started: false, network_used: false,
      hooks_executed: false,
    });
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="workspace-1" />
    </QueryClientProvider>);
    expect(await screen.findByText("src/main.go")).toBeInTheDocument();
    expect(screen.getByText(/REDACTED:api-key/)).toBeInTheDocument();
    expect(screen.queryByText(/workspace root|authority granted/i)).not.toBeInTheDocument();
  });

  it("collapses files independently and keeps change requests tied to the current file and base", async () => {
    const user = userEvent.setup();
    const data = {
      available: true, base_head: "first-head", returned_count: 2,
      total_patch_bytes: 48, omitted_count: 0, redaction_count: 0,
      items: ["src/first.ts", "src/second.ts"].map((path) => ({
        path, content_state: "text", added_lines: 1, deleted_lines: 0,
        patch: `+change in ${path}\n`,
      })),
    };
    const repositoryDiff = vi.fn().mockResolvedValue(data);
    const onRequestChange = vi.fn();
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="workspace-1" onRequestChange={onRequestChange} />
    </QueryClientProvider>);

    const firstSummary = (await screen.findByText("src/first.ts")).closest("summary")!;
    const secondSummary = screen.getByText("src/second.ts").closest("summary")!;
    const firstFile = firstSummary.parentElement!;
    const secondFile = secondSummary.parentElement!;
    expect(firstFile).toHaveAttribute("open");
    expect(secondFile).toHaveAttribute("open");
    expect(within(firstSummary).queryByRole("button")).not.toBeInTheDocument();

    await user.click(firstSummary);
    expect(firstFile).not.toHaveAttribute("open");
    expect(within(firstFile).getByText("+change in src/first.ts")).not.toBeVisible();
    expect(within(secondFile).getByText("+change in src/second.ts")).toBeVisible();

    repositoryDiff.mockResolvedValue({ ...data, base_head: "refreshed-head" });
    await user.click(screen.getByRole("button", { name: "Refresh repository diff" }));
    await waitFor(() => expect(queryClient.getQueryData(["workspace", "workspace-1", "repository-diff"]))
      .toMatchObject({ base_head: "refreshed-head" }));
    expect(firstFile).not.toHaveAttribute("open");
    await user.click(within(secondFile).getByRole("button", { name: "Request changes to this diff" }));
    expect(onRequestChange).toHaveBeenLastCalledWith("src/second.ts", "refreshed-head");
    expect(secondFile).toHaveAttribute("open");

    await user.click(secondSummary);
    await user.click(firstSummary);
    expect(secondFile).not.toHaveAttribute("open");
    expect(within(firstFile).getByText("+change in src/first.ts")).toBeVisible();
    await user.click(within(firstFile).getByRole("button", { name: "Request changes to this diff" }));
    expect(onRequestChange).toHaveBeenLastCalledWith("src/first.ts", "refreshed-head");
    expect(firstFile).toHaveAttribute("open");
  });

  it("keeps file status visible when an omitted patch is collapsed", async () => {
    const user = userEvent.setup();
    const repositoryDiff = vi.fn().mockResolvedValue({
      available: true, base_head: "base-head", returned_count: 1,
      total_patch_bytes: 0, omitted_count: 0, redaction_count: 0,
      items: [{ path: "assets/image.png", content_state: "binary", added_lines: 0, deleted_lines: 0 }],
    });
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="workspace-1" />
    </QueryClientProvider>);
    const summary = (await screen.findByText("assets/image.png")).closest("summary")!;
    const omitted = summary.parentElement!.querySelector(".repository-diff-omitted")!;
    await user.click(summary);
    expect(within(summary).getByText("binary")).toBeVisible();
    expect(omitted).not.toBeVisible();
    await user.click(summary);
    expect(omitted).toBeVisible();
  });
});
