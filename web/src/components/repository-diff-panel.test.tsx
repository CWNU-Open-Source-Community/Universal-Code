import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { LocaleProvider } from "../lib/locale";
import { RepositoryDiffPanel } from "./repository-diff-panel";

describe("RepositoryDiffPanel", () => {
  it("pages without accumulating patches, retries failures, and resets when the workspace changes", async () => {
    const user = userEvent.setup();
    const first = { available: true, base_head: "head", returned_count: 1, total_patch_bytes: 10,
      next_after: "a.txt", omitted_count: 1, redaction_count: 0,
      items: [{ path: "a.txt", staging: "unmodified", worktree: "modified", content_state: "text", patch: "+first", added_lines: 1,
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
    await user.click(screen.getByRole("button", { name: "Refresh repository diff" }));
    await screen.findByText("a.txt");
    expect(repositoryDiff).toHaveBeenLastCalledWith("first-workspace", expect.any(AbortSignal), "");
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh repository diff" })).toHaveAttribute("title", "After working-tree changes, refresh from the first page");
    view.rerender(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="second-workspace" />
    </QueryClientProvider>);
    await screen.findByText("a.txt");
    expect(repositoryDiff).toHaveBeenLastCalledWith("second-workspace", expect.any(AbortSignal), "");
  });

  it("distinguishes full counts on a truncated preview from unavailable counts", async () => {
    const client = { repositoryDiff: vi.fn().mockResolvedValue({ available: true, base_head: "head",
      returned_count: 2, total_patch_bytes: 100, omitted_count: 0, redaction_count: 0,
      items: [{ path: "big.txt", staging: "unmodified", worktree: "modified", content_state: "text", patch: "-old\n", added_lines: 20000,
        deleted_lines: 20000, statistics_complete: true, truncated: true },
      { path: "complex.txt", staging: "unmodified", worktree: "modified", content_state: "computation_limited", patch: "", added_lines: 0,
        deleted_lines: 0, statistics_complete: false }] }) } as unknown as APIClient;
    render(<QueryClientProvider client={new QueryClient()}>
      <RepositoryDiffPanel client={client} workspaceID="stats" />
    </QueryClientProvider>);
    expect(await screen.findByText("20000 added / 20000 deleted")).toBeVisible();
    expect(screen.getByTitle("Counts include all changes in this file")).toBeVisible();
    expect(screen.getByText("Computation limited; line counts unavailable")).toBeVisible();
    expect(screen.queryByText("0 added / 0 deleted")).not.toBeInTheDocument();
  });
  afterEach(() => localStorage.removeItem("prayu.locale.v1"));

  it.each([
    { locale: "en-US", label: "Sensitive changes hidden" },
    { locale: "zh-CN", label: "敏感变更已隐藏" },
  ])("shows a hidden sensitive change without invented patch lines in $locale", async ({ locale, label }) => {
    localStorage.setItem("prayu.locale.v1", locale);
    const client = { repositoryDiff: vi.fn().mockResolvedValue({ available: true, base_head: "head",
      returned_count: 1, total_patch_bytes: 0, redaction_count: 2, omitted_count: 0,
      items: [{ path: "secret.txt", staging: "unmodified", worktree: "modified", content_state: "text",
        patch: "", added_lines: 0, deleted_lines: 0, statistics_complete: false, redacted: true }] }) } as unknown as APIClient;
    render(<LocaleProvider><QueryClientProvider client={new QueryClient()}>
      <RepositoryDiffPanel client={client} workspaceID="hidden" />
    </QueryClientProvider></LocaleProvider>);
    const row = (await screen.findByText("secret.txt")).closest("article")!;
    expect(within(row).getAllByText(label)).toHaveLength(1);
    expect(row.querySelector("details, pre, .repository-diff-file-counts")).toBeNull();
    expect(row).not.toHaveTextContent(/No text changes|无文本差异|@@/);
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
        path, staging: "unmodified", worktree: "modified", content_state: "text", added_lines: 1, deleted_lines: 0,
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
    await user.click(screen.getByRole("button", { name: "Quote src/second.ts" }));
    expect(onRequestChange).toHaveBeenLastCalledWith("src/second.ts", "refreshed-head");
    expect(secondFile).toHaveAttribute("open");

    await user.click(secondSummary);
    await user.click(firstSummary);
    expect(secondFile).not.toHaveAttribute("open");
    expect(within(firstFile).getByText("+change in src/first.ts")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Quote src/first.ts" }));
    expect(onRequestChange).toHaveBeenLastCalledWith("src/first.ts", "refreshed-head");
    expect(firstFile).toHaveAttribute("open");
  });

  it("shows one readable reason and still quotes a file without an empty disclosure", async () => {
    const user = userEvent.setup();
    const onRequestChange = vi.fn();
    const repositoryDiff = vi.fn().mockResolvedValue({
      available: true, base_head: "base-head", returned_count: 1,
      total_patch_bytes: 0, omitted_count: 0, redaction_count: 0,
      items: [{ path: "assets/image.png", staging: "unmodified", worktree: "modified",
        content_state: "binary_or_unsupported", added_lines: 0, deleted_lines: 0 }],
    });
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="workspace-1" onRequestChange={onRequestChange} />
    </QueryClientProvider>);
    const item = (await screen.findByText("assets/image.png")).closest("article")!;
    expect(item.querySelector("details")).toBeNull();
    expect(within(item).getAllByText("Binary or unsupported text format")).toHaveLength(1);
    expect(item).not.toHaveTextContent("binary_or_unsupported");
    await user.click(within(item).getByRole("button", { name: "Quote assets/image.png" }));
    expect(onRequestChange).toHaveBeenCalledWith("assets/image.png", "base-head");
  });

  it.each([
    { locale: "en-US", modified: "Working tree: modified", deleted: "Working tree: deleted", staged: "Index: added", untracked: "untracked", renamed: "Index: renamed", copied: "Index: copied" },
    { locale: "zh-CN", modified: "工作区：修改", deleted: "工作区：删除", staged: "暂存区：新增", untracked: "未跟踪", renamed: "暂存区：重命名", copied: "暂存区：复制" },
  ])("shows non-text change types without zero line statistics in $locale", async ({ locale, modified, deleted, staged, untracked, renamed, copied }) => {
    localStorage.setItem("prayu.locale.v1", locale);
    const repositoryDiff = vi.fn().mockResolvedValue({
      available: true, base_head: "base-head", returned_count: 7,
      total_patch_bytes: 0, omitted_count: 0, redaction_count: 0,
      items: [
        { path: "alpha.bin", staging: "unmodified", worktree: "modified" },
        { path: "bravo.bin", staging: "unmodified", worktree: "deleted" },
        { path: "staged.bin", staging: "added", worktree: "modified" },
        { path: "new.bin", staging: "untracked", worktree: "untracked" },
        { path: "renamed.bin", staging: "renamed", worktree: "unmodified" },
        { path: "copied.bin", staging: "copied", worktree: "unmodified" },
        { path: "large.txt", staging: "unmodified", worktree: "modified", content_state: "size_limited" },
      ].map((item) => ({ content_state: "binary_or_unsupported", added_lines: 0, deleted_lines: 0, ...item })),
    });
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(<LocaleProvider><QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="workspace-1" />
    </QueryClientProvider></LocaleProvider>);
    const alpha = (await screen.findByText("alpha.bin")).closest("article")!;
    expect(alpha).toHaveTextContent(modified);
    expect(screen.getByText("bravo.bin").closest("article")).toHaveTextContent(deleted);
    const stagedSummary = screen.getByText("staged.bin").closest("article")!;
    expect(stagedSummary).toHaveTextContent(staged);
    expect(stagedSummary).toHaveTextContent(modified);
    expect(within(screen.getByText("new.bin").closest("article")!).getAllByText(untracked)).toHaveLength(1);
    expect(screen.getByText("renamed.bin").closest("article")).toHaveTextContent(renamed);
    expect(screen.getByText("copied.bin").closest("article")).toHaveTextContent(copied);
    expect(container.querySelector(".repository-diff-file-counts")).not.toBeInTheDocument();
    expect(container.querySelector("details")).not.toBeInTheDocument();
    expect(container).not.toHaveTextContent(/binary_or_unsupported|size_limited|BINARY OR UNSUPPORTED/);
  });

  it("preserves text statistics and truncation while showing conflict states", async () => {
    localStorage.setItem("prayu.locale.v1", "zh-CN");
    const repositoryDiff = vi.fn().mockResolvedValue({
      available: true, base_head: "base-head", returned_count: 1, truncated: true,
      total_patch_bytes: 32, omitted_count: 0, redaction_count: 0,
      items: [{ path: "conflicted.ts", staging: "conflicted", worktree: "conflicted",
        content_state: "text", added_lines: 4, deleted_lines: 2, truncated: true, statistics_complete: true,
        patch: "@@ diff truncated by repository_diff.v1 @@\n" }],
    });
    const client = { repositoryDiff } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<LocaleProvider><QueryClientProvider client={queryClient}>
      <RepositoryDiffPanel client={client} workspaceID="workspace-1" />
    </QueryClientProvider></LocaleProvider>);
    const summary = (await screen.findByText("conflicted.ts")).closest("summary")!;
    expect(summary).toHaveTextContent("暂存区：有冲突");
    expect(summary).toHaveTextContent("工作区：有冲突");
    expect(summary).toHaveTextContent("4 行新增 / 2 行删除");
    expect(screen.getByText("已截断")).toBeVisible();
    expect(screen.getByText("@@ diff truncated by repository_diff.v1 @@")).toBeVisible();
  });
});
