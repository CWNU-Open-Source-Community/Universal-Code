import { APIClient } from "./client";
import { chatGPTAuthProtocol, chatGPTUsageURL, parseChatGPTAuthStatus, validChatGPTStartURL } from "./chatgpt-auth";

const localURL = `http://127.0.0.1:54321/auth/start/${"a".repeat(43)}`;
const status = () => ({ protocol_version: chatGPTAuthProtocol, available: true,
  store_kind: "windows_credential_manager", accounts: [], usage_url: chatGPTUsageURL });

describe("ChatGPT authentication protocol", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("accepts only the exact one-use local ticket format", () => {
    expect(validChatGPTStartURL(localURL)).toBe(true);
    for (const invalid of [localURL + "?state=secret", localURL + "#fragment",
      localURL.replace("http:", "https:"), localURL.replace("127.0.0.1", "localhost"),
      localURL.replace("127.0.0.1", "127.0.0.1.example.org"), localURL.replace("54321", "65536"),
      localURL.replace("54321", "0"), localURL.replace("54321", "05432"),
      localURL.replace("127.0.0.1", "name@127.0.0.1"), localURL.slice(0, -1),
      localURL + "a", "https://auth.openai.com/authorize?state=secret"]) {
      expect(validChatGPTStartURL(invalid), invalid).toBe(false);
    }
  });

  it("rejects leaked credentials, foreign navigation, and inconsistent account metadata", () => {
    const waiting = { ...status(), login: { id: "attempt", state: "waiting", start_url: localURL } };
    expect(parseChatGPTAuthStatus(waiting)).toEqual(waiting);
    const account = { id: "account-one", label: "Account", email: "a@example.invalid",
      provider: "chatgpt-account-one", state: "connected", models: [{ id: "model", name: "Model" }] };
    for (const bad of [{ ...waiting, access_token: "never-show" },
      { ...waiting, login: { ...waiting.login, state: "succeeded" } },
      { ...waiting, login: { ...waiting.login, state: ["waiting"] } },
      { ...waiting, login: { ...waiting.login, state: "waiting", start_url: "https://evil.invalid" } },
      { ...status(), usage_url: "https://evil.invalid" },
      { ...status(), accounts: [{ ...account, refresh_token: "never-show" }] },
      { ...status(), accounts: [account, account] }]) {
      expect(() => parseChatGPTAuthStatus(bad)).toThrow("ChatGPT 账户状态无效");
    }
  });

  it("uses read status and confirmed control actions without renderer navigation", async () => {
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({
      version: "api.v1", request_id: "fixture", data: status(),
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-token", "/api/v1", "control-token", { chatGPTAuthEnabled: true, runControlEnabled: false });
    await client.chatGPTAuthStatus();
    const base = { version: chatGPTAuthProtocol, confirm: true } as const;
    await client.startChatGPTLogin(base);
    await client.cancelChatGPTLogin({ ...base, attempt_id: "attempt" });
    await client.logoutChatGPTAccount({ ...base, account_id: "account" });
    await client.refreshChatGPTModels({ ...base, account_id: "account" });
    await client.openChatGPTUsage(base);
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/v1/models/chatgpt",
      "/api/v1/models/chatgpt/login", "/api/v1/models/chatgpt/cancel",
      "/api/v1/models/chatgpt/logout", "/api/v1/models/chatgpt/models", "/api/v1/models/chatgpt/usage"]);
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe("Bearer read-token");
    for (const [, init] of fetchMock.mock.calls.slice(1)) {
      expect(init.method).toBe("POST");
      expect(init.headers.Authorization).toBe("Bearer control-token");
      expect(JSON.parse(init.body)).toMatchObject(base);
    }
  });

  it("fails closed without advertised capability, confirmation or a matching action identity", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(new APIClient("read", "/api/v1", "control").chatGPTAuthStatus()).rejects.toThrow();
    await expect(new APIClient("read", "/api/v1", "", { chatGPTAuthEnabled: true }).chatGPTAuthStatus()).rejects.toThrow();
    const client = new APIClient("read", "/api/v1", "control", { chatGPTAuthEnabled: true });
    await expect(client.startChatGPTLogin({ version: chatGPTAuthProtocol, confirm: false } as never)).rejects.toThrow();
    await expect(client.logoutChatGPTAccount({ version: chatGPTAuthProtocol, confirm: true, account_id: "" })).rejects.toThrow();
    await expect(client.openChatGPTUsage({ version: chatGPTAuthProtocol, confirm: true, url: "https://evil.invalid" } as never)).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("accepts account-backed model availability while keeping ordinary provider constraints", async () => {
    const provider = { name: `chatgpt-${"a".repeat(32)}`, display_name: "ChatGPT (account@example.invalid)",
      kind: "openai_compatible", status: "available", custom: false, enabled: true, definition_revision: 0,
      transport: "openai_responses", search_mode: "disabled", native_web_search_capability: "unsupported",
      native_web_search_runtime_enabled: false, credential_source: "chatgpt_oauth", network_required: true,
      configuration_error: false, models: ["test-model"], harnesses: [{ protocol_version: "model_harness.v1",
        model: "test-model", transport_protocol: "openai_responses", tool_strategy: "native", json_strategy: "native",
        qualification_status: "qualification_required", tool_calls_qualified: false, tool_results_qualified: false,
        strict_json_qualified: false, streaming_qualified: false, root_eligible: false, structured_json_eligible: false,
        latest_qualification_status: "not_configured", qualification_checked_at: "", qualification_source: "availability",
        qualified_at: "", expires_at: "" }] };
    const payload = (item: object) => ({ protocol_version: "model_availability.v2", generation: 1, providers: [item], routes: [] });
    const response = (item: object) => new Response(JSON.stringify({ version: "api.v1", request_id: "fixture", data: payload(item) }),
      { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn().mockResolvedValueOnce(response(provider)).mockResolvedValueOnce(response({ ...provider,
      name: "chatgpt", display_name: "ChatGPT", status: "not_configured", models: [], harnesses: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read");
    await expect(client.modelAvailability()).resolves.toEqual(payload(provider));
    await expect(client.modelAvailability()).resolves.toMatchObject({ providers: [{ name: "chatgpt", credential_source: "chatgpt_oauth" }] });
    for (const invalid of [{ ...provider, custom: true }, { ...provider, name: "openai" },
      { ...provider, transport: "openai_chat_completions" }, { ...provider, credential_source: "system" }]) {
      fetchMock.mockResolvedValueOnce(response(invalid));
      await expect(client.modelAvailability()).rejects.toThrow("invalid");
    }
  });
});
