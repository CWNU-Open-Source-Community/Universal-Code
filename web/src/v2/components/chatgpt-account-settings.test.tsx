import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { chatGPTAuthProtocol, chatGPTUsageURL, type ChatGPTAuthStatusView } from "../../api/chatgpt-auth";
import { V2ModelSettings } from "./model-settings";
import { chatGPTStatusQueryKey } from "./chatgpt-account-settings";

const localURL = `http://127.0.0.1:54321/auth/start/${"a".repeat(43)}`;
const base = { version: chatGPTAuthProtocol, confirm: true } as const;
const account = (id = "account-one", state = "connected" as const) => ({ id,
  label: `${id}@example.invalid`, email: `${id}@example.invalid`, provider: `chatgpt-${id}`, state,
  models: [{ id: "test-model", name: "Test model" }, { id: "second-model", name: "Second model" }] });
const initialStatus = (): ChatGPTAuthStatusView => ({ protocol_version: chatGPTAuthProtocol,
  available: true, store_kind: "windows_credential_manager", accounts: [], usage_url: chatGPTUsageURL });

function fixture(initial = initialStatus(), prepareForDraft = false) {
  let status = initial;
  const chatGPTAuthStatus = vi.fn().mockImplementation(async () => status);
  const startChatGPTLogin = vi.fn().mockImplementation(async () => {
    status = { ...status, login: { id: "attempt-one", state: "waiting", start_url: localURL } };
    return status;
  });
  const cancelChatGPTLogin = vi.fn().mockImplementation(async () => {
    status = { ...status, login: { id: "attempt-one", state: "cancelled" } };
    return status;
  });
  const logoutChatGPTAccount = vi.fn().mockImplementation(async ({ account_id }) => {
    status = { ...status, accounts: status.accounts.map((item) => item.id === account_id
      ? { ...item, state: "signed_out", models: [] } : item) };
    return status;
  });
  const refreshChatGPTModels = vi.fn().mockImplementation(async () => status);
  const openChatGPTUsage = vi.fn().mockImplementation(async () => status);
  const qualifyModelHarness = vi.fn().mockResolvedValue({ status: "qualified", failure_reason: "none", harness: { root_eligible: true } });
  const availableModelRoutes = vi.fn().mockImplementation(async () => ({ routes: status.accounts.flatMap((item) =>
    item.models.map((model) => ({ provider_id: item.provider, model: model.id, selectable: item.state === "connected" }))) }));
  const providerDefinitions = vi.fn();
  const client = { hasChatGPTAuth: true, hasModelControl: true, chatGPTAuthStatus, startChatGPTLogin,
    cancelChatGPTLogin, logoutChatGPTAccount, refreshChatGPTModels, openChatGPTUsage,
    qualifyModelHarness, availableModelRoutes, providerDefinitions } as unknown as APIClient;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const onReady = vi.fn();
  const view = render(<QueryClientProvider client={queryClient}>
    <V2ModelSettings client={client} presets={[]} onSelectPreset={vi.fn()} prepareForDraft={prepareForDraft} onReady={onReady} />
  </QueryClientProvider>);
  return { ...view, client, queryClient, onReady, chatGPTAuthStatus, startChatGPTLogin, cancelChatGPTLogin,
    logoutChatGPTAccount, refreshChatGPTModels, openChatGPTUsage, qualifyModelHarness, availableModelRoutes,
    providerDefinitions, setStatus: (next: ChatGPTAuthStatusView) => { status = next; } };
}

async function openAccount(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /ChatGPT，订阅账户/u }));
  await screen.findByRole("heading", { name: "ChatGPT 账户" });
}

describe("ChatGPT account settings", () => {
  it("starts in Go, copies only a local ticket, and sends fixed usage/cancel actions", async () => {
    const user = userEvent.setup();
    const controls = fixture();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const clipboard = vi.spyOn(navigator.clipboard, "writeText");
    await openAccount(user);
    await waitFor(() => expect(screen.getByRole("button", { name: "Continue with ChatGPT" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
    expect(await screen.findByText("等待 ChatGPT 登录…")).toBeVisible();
    expect(controls.startChatGPTLogin).toHaveBeenCalledWith(base);
    await waitFor(() => expect(screen.getByRole("button", { name: "复制登录链接" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "复制登录链接" }));
    expect(clipboard).toHaveBeenCalledWith(localURL);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(open).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: /Manage usage/u }));
    await waitFor(() => expect(controls.openChatGPTUsage).toHaveBeenCalledWith(base));
    await waitFor(() => expect(screen.getByRole("button", { name: "取消登录" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "取消登录" }));
    await screen.findByText("登录已取消。");
    expect(controls.cancelChatGPTLogin).toHaveBeenCalledWith({ ...base, attempt_id: "attempt-one" });
    expect(screen.queryByText(localURL)).not.toBeInTheDocument();
    expect(controls.providerDefinitions).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it("resumes a pending login and refreshes account and route state while back in the catalog", async () => {
    const user = userEvent.setup();
    const pending = { ...initialStatus(), login: { id: "attempt-one", state: "waiting" as const, start_url: localURL } };
    const controls = fixture(pending);
    const invalidate = vi.spyOn(controls.queryClient, "invalidateQueries");
    await openAccount(user);
    await screen.findByText("等待 ChatGPT 登录…");
    await user.click(screen.getByRole("button", { name: "返回模型选择" }));
    const card = screen.getByRole("button", { name: /ChatGPT，订阅账户，等待登录/u });
    await waitFor(() => expect(card).toHaveFocus());
    controls.setStatus({ ...initialStatus(), accounts: [account()], login: { id: "attempt-one", state: "succeeded", account_id: "account-one" } });
    await screen.findByRole("button", { name: /ChatGPT，订阅账户，账户已连接/u }, { timeout: 2500 });
    expect(controls.startChatGPTLogin).not.toHaveBeenCalled();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["v2", "models", "available-routes"] });
    await openAccount(user);
    expect(screen.getByRole("combobox", { name: "ChatGPT 账户" })).toHaveValue("account-one");
  });

  it.each(["qualified", "rejected", "unselectable"])("returns a draft model only after both checks: %s", async (outcome) => {
    const user = userEvent.setup();
    const controls = fixture({ ...initialStatus(), accounts: [account(), account("account-two")] }, true);
    if (outcome === "rejected") controls.qualifyModelHarness.mockResolvedValue({ status: "failed", failure_reason: "tool_call_failed", harness: { root_eligible: false } });
    if (outcome === "unselectable") controls.availableModelRoutes.mockResolvedValue({ routes: [{ provider_id: "chatgpt-account-two", model: "second-model", selectable: false }] });
    await openAccount(user);
    await user.selectOptions(await screen.findByRole("combobox", { name: "ChatGPT 账户" }), "account-two");
    await user.selectOptions(screen.getByRole("combobox", { name: "ChatGPT 模型" }), "second-model");
    await user.click(screen.getByRole("button", { name: "检查并返回任务" }));
    await waitFor(() => expect(controls.qualifyModelHarness).toHaveBeenCalledWith({ version: "model_harness_qualification.v1", provider: "chatgpt-account-two", model: "second-model", confirm_qualification: true }));
    if (outcome === "qualified") {
      await waitFor(() => expect(controls.onReady).toHaveBeenCalledWith({ id: "chatgpt-account-two", default_model: "second-model" }));
    } else {
      expect(await screen.findByRole("alert")).toHaveTextContent(outcome === "rejected" ? "模型检查未通过" : "尚未确认可用");
      expect(controls.onReady).not.toHaveBeenCalled();
    }
    expect(controls.providerDefinitions).not.toHaveBeenCalled();
  });

  it("allows recovery and logout of pending credentials without selecting an unverified model", async () => {
    const user = userEvent.setup();
    const controls = fixture({ ...initialStatus(), accounts: [{ ...account(), state: "verification_pending" }] });
    await openAccount(user);
    expect(await screen.findByRole("button", { name: "刷新模型" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "检查模型" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "刷新模型" }));
    await waitFor(() => expect(controls.refreshChatGPTModels).toHaveBeenCalledWith({ ...base, account_id: "account-one" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "退出账户" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "退出账户" }));
    await waitFor(() => expect(controls.logoutChatGPTAccount).toHaveBeenCalledWith({ ...base, account_id: "account-one" }));
    expect(await screen.findByRole("button", { name: "Continue with ChatGPT，重新登录所选账户" })).toBeVisible();
    expect(screen.getByRole("combobox", { name: "ChatGPT 模型" })).toBeDisabled();
  });

  it("keeps saved models unusable when current account status cannot be read", async () => {
    const user = userEvent.setup();
    const controls = fixture({ ...initialStatus(), accounts: [account()] });
    await openAccount(user);
    await screen.findByRole("combobox", { name: "ChatGPT 模型" });
    controls.chatGPTAuthStatus.mockRejectedValue(new Error("offline"));
    await controls.queryClient.invalidateQueries({ queryKey: chatGPTStatusQueryKey });
    expect(await screen.findByRole("alert")).toHaveTextContent("无法读取 ChatGPT 账户状态");
    expect(screen.queryByRole("button", { name: "检查模型" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue with ChatGPT" })).toBeDisabled();
  });
});
