import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import type { APIClient } from "../../api/client";
import type { ProviderDefinitionView } from "../../api/types";
import { V2ProviderSettings, validProviderEndpointURL, type V2ProviderDraftPreset } from "./provider-settings";

const openAIPreset: V2ProviderDraftPreset = {
  id: "official-openai",
  displayName: "OpenAI",
  note: "官方 API",
  websiteURL: "https://platform.openai.com",
  endpointURL: "https://api.openai.com/v1/responses",
  transport: "openai_responses",
  models: ["gpt-5", "gpt-5-mini"],
  defaultModel: "gpt-5",
  searchMode: "provider_native",
  nativeSearchDeclared: true,
  advancedConfig: { request_body: { store: false } },
};

function provider(overrides: Partial<ProviderDefinitionView> = {}): ProviderDefinitionView {
  return {
    version: "provider_definition.v1",
    id: "acme",
    display_name: "Acme AI",
    note: "团队账号",
    website_url: "https://acme.example",
    endpoint_url: "https://api.acme.example/v1/chat/completions",
    default_model: "acme-pro",
    models: ["acme-pro"],
    transport: "openai_chat_completions",
    search_mode: "auto",
    native_web_search_capability: "unsupported",
    enabled: true,
    revision: 4,
    advanced_config: {},
    ...overrides,
  };
}

function createClient(providers: ProviderDefinitionView[] = []) {
  let revision = providers.length ? 7 : 0;
  const providerDefinitions = vi.fn().mockImplementation(async () => ({
    version: "provider_definition_collection.v1", revision, providers,
  }));
  const providerCredentialStatuses = vi.fn().mockResolvedValue({
    protocol_version: "provider_credential.v1",
    items: providers.map((definition) => ({
      protocol_version: "provider_credential.v1", provider: definition.id,
      configured: true, store_available: true, store_kind: "windows_credential_manager",
      plaintext_returned: false, restart_required: false, registry_reloaded: false,
      registry_generation: 2,
    })),
  });
  const changeProviderCredential = vi.fn().mockImplementation(async (id, body) => ({
    protocol_version: "provider_credential.v1", provider: id,
    configured: body.action === "set", store_available: true,
    store_kind: "windows_credential_manager", plaintext_returned: false,
    restart_required: false, registry_reloaded: true, registry_generation: 3,
  }));
  const upsertProviderDefinition = vi.fn().mockImplementation(async (_id, body) => {
    revision += 1;
    const saved = { ...body.definition, revision: body.definition.revision + 1 };
    providers = [...providers.filter((item) => item.id !== saved.id), saved]
      .sort((left, right) => left.id.localeCompare(right.id));
    return {
      protocol_version: "provider_definition_control.v1", registry_reloaded: true,
      registry_generation: 3, definition: saved,
      collection: { version: "provider_definition_collection.v1", revision, providers },
    };
  });
  const deleteProviderDefinition = vi.fn().mockImplementation(async (id) => {
    revision += 1;
    providers = providers.filter((item) => item.id !== id);
    return {
      protocol_version: "provider_definition_control.v1", registry_reloaded: true,
      registry_generation: 3, deleted_id: id,
      collection: { version: "provider_definition_collection.v1", revision, providers },
    };
  });
  const diagnoseProvider = vi.fn().mockResolvedValue({
    protocol_version: "provider_diagnostic.v1",
    provider: "acme",
    model: "acme-pro",
    status: "reachable",
    outcome: "success",
    failure_reason: "none",
    retryable: false,
    network_request_attempted: true,
    model_called: true,
    tool_called: false,
    response_content_returned: false,
    duration_ms: 12,
    qualification_status: "available",
  });
  const qualifyModelHarness = vi.fn().mockImplementation(async (body) => ({
    protocol_version: "model_harness_qualification.v1",
    provider: body.provider,
    model: body.model,
    status: "qualified",
    outcome: "success",
    failure_reason: "none",
    retryable: false,
    network_request_attempted: true,
    model_calls: 2,
    synthetic_tool_calls: 1,
    tool_executed: false,
    response_content_returned: false,
    duration_ms: 25,
    qualification_status: "available",
    harness: {
      protocol_version: "model_harness.v1",
      model: body.model,
      transport_protocol: "openai_chat_completions",
      tool_strategy: "native",
      json_strategy: "native",
      qualification_status: "verified",
      latest_qualification_status: "available",
      qualification_checked_at: "2026-08-31T08:00:00Z",
      qualification_source: "harness_qualification",
      tool_calls_qualified: true,
      tool_results_qualified: true,
      strict_json_qualified: true,
      streaming_qualified: true,
      root_eligible: true,
      structured_json_eligible: true,
      qualified_at: "2026-08-31T08:00:00Z",
      expires_at: "2026-09-07T08:00:00Z",
    },
  }));
  const availableModelRoutes = vi.fn().mockImplementation(async () => ({
    protocol_version: "model_route_catalog.v1",
    generation: 3,
    routes: providers.flatMap((definition) => definition.models.map((model) => ({
      provider_id: definition.id, provider_name: definition.display_name, model,
	  definition_revision: definition.revision,
      enabled: definition.enabled, credential_status: "configured",
      qualification_status: "available", harness_ready: true, selectable: true,
      unavailable_reason: "", default_for_routes: [],
      vision_capability: { state: "unknown", source: "unknown" },
    }))),
  }));
  const discoverProviderModels = vi.fn().mockResolvedValue({
    version: "provider_model_discovery.v1", source: "provider_api", models: [], truncated: false,
  });
  return {
    client: {
      hasModelControl: true,
      hasProviderDefinitions: true,
      hasProviderCredentials: true,
      providerDefinitions,
      providerCredentialStatuses,
      changeProviderCredential,
      upsertProviderDefinition,
      deleteProviderDefinition,
      diagnoseProvider,
      qualifyModelHarness,
      availableModelRoutes,
      discoverProviderModels,
    } as unknown as APIClient,
    providerDefinitions,
    providerCredentialStatuses,
    changeProviderCredential,
    upsertProviderDefinition,
    deleteProviderDefinition,
    diagnoseProvider,
    qualifyModelHarness,
    availableModelRoutes,
    discoverProviderModels,
  };
}

function renderSettings(client: APIClient) {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  return render(<QueryClientProvider client={queryClient}>
    <V2ProviderSettings client={client} />
  </QueryClientProvider>);
}

async function fillRequiredProvider(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("供应商 ID"), "nova");
  await user.type(screen.getByLabelText("显示名称"), "Nova Cloud");
  await user.type(screen.getByLabelText("请求地址"), "https://api.nova.example/v1/chat/completions");
  await user.type(screen.getByLabelText("模型列表"), "nova-pro");
}

describe("V2 custom Provider settings", () => {
  it("keeps model catalog and output customization collapsed by default and allows them after expanding advanced settings", async () => {
    const controls = createClient();
    controls.discoverProviderModels.mockResolvedValue({ models: [{ id: "gpt-6-astra" }], truncated: false });
    const user = userEvent.setup();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ProviderSettings client={controls.client} initialPreset={{ ...openAIPreset, models: ["gpt-6.1-sol"], defaultModel: "gpt-6.1-sol" }} />
    </QueryClientProvider>);
    await screen.findByRole("heading", { name: "添加供应商" });
    expect(screen.queryByLabelText("请求地址")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "获取模型列表" })).not.toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "gpt-6.1-sol 默认输出 token" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("API Key")).toBeInTheDocument();
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-6.1-sol");
    expect(screen.getByRole("button", { name: "保存" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByRole("spinbutton", { name: "gpt-6.1-sol 默认输出 token" })).toHaveValue(16384);
    expect(screen.getByRole("spinbutton", { name: "gpt-6.1-sol 单次输出上限 token" })).toHaveValue(128000);
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "transient-quick-key" } });
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    await user.click(screen.getByRole("checkbox", { name: "选择模型 gpt-6-astra" }));
    await user.click(screen.getByRole("button", { name: "添加所选模型" }));
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-6.1-sol");
    await user.click(screen.getByRole("checkbox", { name: "gpt-6-astra 自定义输出限制" }));
    expect(screen.getByRole("checkbox", { name: "选择模型 gpt-6-astra" })).toBeChecked();
    fireEvent.change(screen.getByRole("spinbutton", { name: "gpt-6-astra 单次输出上限 token" }), { target: { value: "64000" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.advanced_config.model_context_windows).toEqual({
      "gpt-6-astra": { window_tokens: 1050000, default_output_tokens: 16384, max_output_tokens: 64000 },
    });
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it("invalidates cached qualification and route state after saving a changed output policy", async () => {
    const controls = createClient([provider()]);
    const user = userEvent.setup();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const availabilityKey = ["models", "availability"];
    const routesKey = ["v2", "models", "available-routes"];
    const threadRouteKey = ["v2", "thread", "thread-1", "model-route"];
    for (const key of [availabilityKey, routesKey, threadRouteKey]) queryClient.setQueryData(key, { qualification: "old-revision" });
    render(<QueryClientProvider client={queryClient}><V2ProviderSettings client={controls.client} /></QueryClientProvider>);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("checkbox", { name: "acme-pro 自定义输出限制" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await screen.findByRole("button", { name: /Acme AI/u });
    for (const key of [availabilityKey, routesKey, threadRouteKey]) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it("aborts an outstanding discovery when the settings component unmounts", async () => {
    const controls = createClient([provider()]);
    controls.discoverProviderModels.mockImplementation(() => new Promise(() => {}));
    const user = userEvent.setup();
    const view = renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    const signal = controls.discoverProviderModels.mock.calls[0][1] as AbortSignal;
    view.unmount();
    expect(signal.aborted).toBe(true);
  });

  it("blocks an invalid numeric policy on submit and lets the user correct the same form", async () => {
    const controls = createClient([provider()]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("checkbox", { name: "acme-pro 自定义输出限制" }));
    fireEvent.change(screen.getByRole("spinbutton", { name: "acme-pro 单次输出上限 token" }), { target: { value: "31744" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(screen.getAllByRole("alert").some((node) => node.textContent?.includes("1024 token"))).toBe(true);
    expect(screen.getByRole("spinbutton", { name: "acme-pro 单次输出上限 token" })).toBeEnabled();
    fireEvent.change(screen.getByRole("spinbutton", { name: "acme-pro 单次输出上限 token" }), { target: { value: "31743" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
  });

  it("discovers using an existing credential revision and merges selected models without changing saved model options", async () => {
    const advanced = { model_capabilities: { "acme-pro": { vision: "supported" } },
      model_context_windows: { "acme-pro": { window_tokens: 100000, default_output_tokens: 8000, max_output_tokens: 32000 } },
      extension: { keep: true } };
    const controls = createClient([provider({ advanced_config: advanced })]);
    controls.discoverProviderModels.mockResolvedValue({ version: "provider_model_discovery.v1", source: "provider_api",
      models: [{ id: "acme-pro" }, { id: "acme-new", input_token_limit: 200000, output_token_limit: 64000 }], truncated: true });
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    expect(controls.discoverProviderModels).toHaveBeenCalledWith({ version: "provider_model_discovery.v1", provider_id: "acme",
      endpoint_url: "https://api.acme.example/v1/chat/completions", transport: "openai_chat_completions",
      advanced_config: advanced, confirm_discovery: true, expected_definition_revision: 4 }, expect.any(AbortSignal));
    expect(screen.getByText(/本次结果未完整返回/u)).toBeInTheDocument();
    expect(screen.getByText(/输入 200,000 token · 输出 64,000 token/u)).toBeInTheDocument();
    expect(screen.getByLabelText("模型列表")).toHaveValue("acme-pro");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
    await user.click(screen.getByRole("checkbox", { name: "选择模型 acme-new" }));
    await user.click(screen.getByRole("button", { name: "添加所选模型" }));
    expect(screen.getByLabelText("模型列表")).toHaveValue("acme-pro\nacme-new");
    expect(screen.getByLabelText("默认模型")).toHaveValue("acme-pro");
    expect(JSON.parse((screen.getByRole("textbox", { name: "高级 JSON" }) as HTMLTextAreaElement).value)).toEqual(advanced);
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition).toMatchObject({ models: ["acme-pro", "acme-new"], default_model: "acme-pro", advanced_config: advanced });
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it.each(["请求地址", "API Key", "协议", "高级 JSON"])("aborts model discovery and discards a stale response when %s changes", async (field) => {
    const controls = createClient([provider()]);
    let resolve!: (value: unknown) => void;
    controls.discoverProviderModels.mockImplementation(() => new Promise((done) => { resolve = done; }));
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    const signal = controls.discoverProviderModels.mock.calls[0][1] as AbortSignal;
    fireEvent.change(field === "高级 JSON" ? screen.getByRole("textbox", { name: field }) : screen.getByLabelText(field), { target: { value: ({ "请求地址": "https://new.example/v1", "API Key": "new-transient-key", "协议": "openai_responses", "高级 JSON": '{"extension":true}' })[field] } });
    expect(signal.aborted).toBe(true);
    resolve({ version: "provider_model_discovery.v1", source: "provider_api", models: [{ id: "stale-model" }], truncated: false });
    await waitFor(() => expect(screen.getByRole("button", { name: "获取模型列表" })).toBeEnabled());
    expect(screen.queryByRole("checkbox", { name: "选择模型 stale-model" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("模型列表")).toHaveValue("acme-pro");
  });

  it("aborts discovery on close and rejects late results after opening a different provider", async () => {
    const controls = createClient([provider(), provider({ id: "other", display_name: "Other AI", models: ["other-model"], default_model: "other-model" })]);
    let resolve!: (value: unknown) => void;
    controls.discoverProviderModels.mockImplementation(() => new Promise((done) => { resolve = done; }));
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    const signal = controls.discoverProviderModels.mock.calls[0][1] as AbortSignal;
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(signal.aborted).toBe(true);
    await user.click(screen.getByRole("button", { name: /Other AI/u }));
    resolve({ models: [{ id: "stale-model" }], truncated: false });
    await waitFor(() => expect(screen.getByLabelText("模型列表")).toHaveValue("other-model"));
    expect(screen.queryByRole("checkbox", { name: "选择模型 stale-model" })).not.toBeInTheDocument();
  });

  it("never discovers a changed endpoint using its old stored credential", async () => {
    const controls = createClient([provider()]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    fireEvent.change(screen.getByLabelText("请求地址"), { target: { value: "https://new.example/v1" } });
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    expect(controls.discoverProviderModels).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("请求地址或协议已变更");
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "new-key-for-new-endpoint" } });
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    expect(controls.discoverProviderModels.mock.calls[0][0]).toMatchObject({ secret: "new-key-for-new-endpoint", endpoint_url: "https://new.example/v1" });
    expect(controls.discoverProviderModels.mock.calls[0][0]).not.toHaveProperty("expected_definition_revision");
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
    expect(screen.getByLabelText("API Key")).toHaveValue("new-key-for-new-endpoint");
  });

  it("keeps failed-discovery drafts intact and supports explicit manual additions and Enter", async () => {
    const controls = createClient([provider()]);
    controls.discoverProviderModels.mockRejectedValue(new Error("服务暂时无法访问"));
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("服务暂时无法访问");
    expect(screen.getByLabelText("模型列表")).toHaveValue("acme-pro");
    fireEvent.change(screen.getByLabelText("手动添加模型"), { target: { value: "acme-new" } });
    await user.click(screen.getByRole("button", { name: "添加模型" }));
    fireEvent.change(screen.getByLabelText("手动添加模型"), { target: { value: "acme-pro, acme-next" } });
    fireEvent.keyDown(screen.getByLabelText("手动添加模型"), { key: "Enter" });
    expect(screen.getByLabelText("模型列表")).toHaveValue("acme-pro\nacme-new\nacme-next");
    expect(screen.getByLabelText("手动添加模型")).toHaveValue("");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
  });

  it("refuses an over-limit model merge without silently truncating the draft or selection", async () => {
    const models = Array.from({ length: 128 }, (_, index) => `model-${index}`);
    const controls = createClient([provider({ models, default_model: models[0] })]);
    controls.discoverProviderModels.mockResolvedValue({ models: [{ id: "one-too-many" }], truncated: false });
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "获取模型列表" }));
    await user.click(screen.getByRole("checkbox", { name: "选择模型 one-too-many" }));
    await user.click(screen.getByRole("button", { name: "添加所选模型" }));
    expect(screen.getByRole("alert")).toHaveTextContent("129 个模型，超过 128 个上限");
    expect(screen.getByLabelText("模型列表")).toHaveValue(models.join("\n"));
    expect(screen.getByRole("checkbox", { name: "选择模型 one-too-many" })).toBeChecked();
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
  });

  it("edits one shared wire-model policy, saves it, and can restore inheritance without removing extensions", async () => {
    const advanced = { model_mapping: { "acme-pro": "wire-model", "acme-alias": "wire-model" },
      model_capabilities: { "acme-pro": { vision: "supported" } }, extension: { keep: true } };
    const controls = createClient([provider({ models: ["acme-pro", "acme-alias"], advanced_config: advanced })]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    expect(screen.getAllByRole("article", { name: "wire-model 输出策略" })).toHaveLength(1);
    expect(screen.getByRole("spinbutton", { name: "wire-model 单次输出上限 token" })).toHaveValue(4096);
    expect(screen.getByText(/服务端实际限制未知/u)).toBeInTheDocument();
    await user.click(screen.getByRole("checkbox", { name: "wire-model 自定义输出限制" }));
    fireEvent.change(screen.getByRole("spinbutton", { name: "wire-model 默认输出 token" }), { target: { value: "8000" } });
    fireEvent.change(screen.getByRole("spinbutton", { name: "wire-model 单次输出上限 token" }), { target: { value: "16000" } });
    expect(screen.getByText("用户覆盖")).toBeInTheDocument();
    expect(JSON.parse((screen.getByRole("textbox", { name: "高级 JSON" }) as HTMLTextAreaElement).value)).toEqual({ ...advanced,
      model_context_windows: { "wire-model": { window_tokens: 32768, default_output_tokens: 8000, max_output_tokens: 16000 } } });
    await user.click(screen.getByRole("checkbox", { name: "wire-model 自定义输出限制" }));
    expect(JSON.parse((screen.getByRole("textbox", { name: "高级 JSON" }) as HTMLTextAreaElement).value)).toEqual(advanced);
    await user.click(screen.getByRole("checkbox", { name: "wire-model 自定义输出限制" }));
    fireEvent.change(screen.getByRole("spinbutton", { name: "wire-model 单次输出上限 token" }), { target: { value: "24000" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.advanced_config).toEqual({ ...advanced,
      model_context_windows: { "wire-model": { window_tokens: 32768, default_output_tokens: 1024, max_output_tokens: 24000 } } });
  });

  it("keeps malformed handwritten model policy intact and blocks lossy form changes", async () => {
    const advanced = { model_context_windows: { "acme-pro": { window_tokens: 32768, default_output_tokens: 1024, max_output_tokens: 4096, future: true } } };
    const controls = createClient([provider({ advanced_config: advanced })]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    expect(screen.getByRole("checkbox", { name: "acme-pro 自定义输出限制" })).toBeDisabled();
    expect(screen.getByRole("spinbutton", { name: "acme-pro 默认输出 token" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(JSON.parse((screen.getByRole("textbox", { name: "高级 JSON" }) as HTMLTextAreaElement).value)).toEqual(advanced);
  });

  it("keeps image empty and invalid-JSON states inside the same padded field grid", async () => {
    const controls = createClient();
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    const section = screen.getByRole("region", { name: "图片输入" });
    const empty = within(section).getByText("先填写模型列表。");
    expect(empty.parentElement).toHaveClass("v2-provider-grid");
    expect(empty).toHaveClass("v2-provider-field-help", "is-wide");
    fireEvent.change(screen.getByRole("textbox", { name: "高级 JSON" }), { target: { value: "{" } });
    expect(within(section).getByText("先修正下方高级 JSON，再设置图片能力。").parentElement).toBe(empty.parentElement);
  });

  it.each(["remove", "rename"])("keeps image declarations aligned when models %s without transferring vision support", async (action) => {
    const controls = createClient([provider({ models: ["acme-pro", "acme-image"],
      advanced_config: { custom_extension: { keep: "unchanged" }, model_capabilities: {
        "acme-pro": { vision: "unknown" }, "acme-image": { vision: "supported" },
      } } })]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    fireEvent.change(screen.getByLabelText("模型列表"), { target: { value: action === "remove" ? "acme-pro" : "acme-pro\nacme-renamed" } });
    if (action === "rename") expect(screen.getByRole("combobox", { name: "acme-renamed 图片输入" })).toHaveValue("unknown");
    const expected = { custom_extension: { keep: "unchanged" }, model_capabilities: { "acme-pro": { vision: "unknown" } } };
    expect(JSON.parse((screen.getByRole("textbox", { name: "高级 JSON" }) as HTMLTextAreaElement).value)).toEqual(expected);
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.advanced_config).toEqual(expected);
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });
  it.each([
    { model_capabilities: "handwritten-invalid" },
    { model_capabilities: { "acme-pro": { vision: "supported", future_detail: "do not discard" } } },
  ])("preserves invalid handwritten image configuration through model edits and blocks lossy controls", async (advancedConfig) => {
    const controls = createClient([provider({ advanced_config: advancedConfig })]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    const original = (screen.getByRole("textbox", { name: "高级 JSON" }) as HTMLTextAreaElement).value;
    expect(screen.getByRole("combobox", { name: "acme-pro 图片输入" })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("模型列表"), { target: { value: "acme-renamed" } });
    await user.selectOptions(screen.getByLabelText("默认模型"), "acme-renamed");
    expect(screen.getByRole("textbox", { name: "高级 JSON" })).toHaveValue(original);
    expect(screen.getByRole("combobox", { name: "acme-renamed 图片输入" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("原内容已保留");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it("accepts HTTPS and literal loopback HTTP model endpoints without URL normalization aliases", () => {
    for (const endpoint of ["https://api.example.com/v1/chat/completions",
      "http://127.0.0.1:18867/v1/chat/completions", "http://127.2.3.4:80/v1",
      "http://localhost:18867/v1", "http://LOCALHOST/v1", "http://[::1]:18867/v1",
      "http://[0:0:0:0:0:0:0:1]/v1", "http://[::ffff:127.0.0.1]:18867/v1"]) {
      expect(validProviderEndpointURL(endpoint), endpoint).toBe(true);
    }
    for (const endpoint of ["http://api.example.com/v1", "http://192.168.1.2/v1", "http://10.0.0.1/v1",
      "http://127.0.0.1.example.com/v1", "http://localhost.example.com/v1", "http://localhost./v1",
      "http://127.1/v1", "http://2130706433/v1", "http://0x7f000001/v1", "http://127.00.0.1/v1",
      "http://%6cocalhost/v1", "http://%31%32%37.0.0.1/v1", "http://127.0.0.1@evil.example/v1",
      "http://evil.example@localhost/v1", "http://127.0.0.1\\@evil.example/v1", "http://[::2]/v1",
      "http://[::ffff:192.168.1.2]/v1", "https://user:pass@api.example.com/v1", "https://@api.example.com/v1",
      "https://api.example.com/v1?key=secret", "http://localhost/v1#fragment", "http://localhost:/v1",
      "http://local\nhost/v1", "http:localhost/v1", "ftp://localhost/v1"]) {
      expect(validProviderEndpointURL(endpoint), endpoint).toBe(false);
    }
  });

  it("saves an exact local HTTP endpoint while keeping website links HTTPS-only", async () => {
    const controls = createClient();
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    const endpoint = "http://127.0.0.1:18867/v1/chat/completions";
    fireEvent.change(screen.getByLabelText("请求地址"), { target: { value: endpoint } });
    fireEvent.change(screen.getByLabelText("官网链接"), { target: { value: "http://localhost/" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("官网链接必须是 HTTPS URL");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("官网链接"), { target: { value: "" } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.endpoint_url).toBe(endpoint);
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it("diagnoses and qualifies a saved keyless local model without writing a credential", async () => {
    const controls = createClient([provider({ endpoint_url: "http://127.0.0.1:18867/v1/chat/completions",
      advanced_config: { operator_notes: { $credential: "unused-extension-data" } } })]);
    controls.providerCredentialStatuses.mockResolvedValue({ protocol_version: "provider_credential.v1", items: [] });
    const user = userEvent.setup();
    renderSettings(controls.client);
    const row = await screen.findByRole("button", { name: /Acme AI/u });
    expect(within(row).getByText("本机 · 可不填密钥")).toBeInTheDocument();
    await user.click(row);
    expect(screen.getByText(/本机模型未引用凭据，可留空并进行连接验证/u)).toBeInTheDocument();
    const verify = screen.getByRole("button", { name: "测试并验证 Harness" });
    expect(verify).toBeEnabled();
    await user.click(verify);
    const dialog = screen.getByRole("dialog", { name: "测试并验证 Harness？" });
    await user.click(within(dialog).getByRole("button", { name: "开始验证" }));
    await waitFor(() => expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(1));
    expect(controls.diagnoseProvider).toHaveBeenCalledTimes(1);
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it.each([
    { endpoint_url: "https://api.example.com/v1", advanced_config: {} },
    { endpoint_url: "http://localhost:18867/v1", advanced_config: {
      request_headers: { Authorization: { $credential: "acme", template: "Bearer ${secret}" } },
    } },
    { endpoint_url: "http://[::1]:18867/v1", advanced_config: {
      request_body: { nested: [{ $credential: "acme" }] },
    } },
  ])("requires missing credentials for remote or credential-referencing models ($endpoint_url)", async (overrides) => {
    const controls = createClient([provider(overrides)]);
    controls.providerCredentialStatuses.mockResolvedValue({ protocol_version: "provider_credential.v1", items: [] });
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    expect(screen.getByRole("button", { name: "测试并验证 Harness" })).toBeDisabled();
    expect(screen.getByText(/请先把 API Key 保存到系统凭据管理器/u)).toBeInTheDocument();
    expect(controls.diagnoseProvider).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it("opens a new preset only after definitions load and does not overwrite operator edits", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    const onExit = vi.fn();
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    const view = render(<QueryClientProvider client={queryClient}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset} onExit={onExit} />
    </QueryClientProvider>);

    expect(screen.getByRole("heading", { name: "连接 OpenAI" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "添加供应商" })).toBeInTheDocument();
    expect(screen.queryByLabelText("供应商 ID")).not.toBeInTheDocument();
    expect(screen.getByLabelText("API Key")).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("供应商 ID")).toHaveValue("official-openai");
    expect(screen.getByLabelText("请求地址")).toHaveValue("https://api.openai.com/v1/responses");
    expect(screen.getByLabelText("协议")).toHaveValue("openai_responses");
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-5");
    expect(screen.getByRole("textbox", { name: "高级 JSON" })).toHaveValue(
      JSON.stringify({ request_body: { store: false } }, null, 2),
    );

    await user.clear(screen.getByLabelText("显示名称"));
    await user.type(screen.getByLabelText("显示名称"), "我的 OpenAI");
    await user.click(screen.getByRole("button", { name: "收起高级设置" }));
    await user.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("显示名称")).toHaveValue("我的 OpenAI");
    const changedPreset = { ...openAIPreset, displayName: "不应覆盖用户输入" };
    view.rerender(<QueryClientProvider client={queryClient}>
      <V2ProviderSettings client={controls.client} initialPreset={changedPreset} onExit={onExit} />
    </QueryClientProvider>);
    expect(screen.getByLabelText("显示名称")).toHaveValue("我的 OpenAI");

    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(onExit).toHaveBeenCalledTimes(1);
  });

  it("edits the stored definition instead of replacing it with a same-ID preset", async () => {
    const user = userEvent.setup();
    const existing = provider({
      id: "official-openai",
      display_name: "团队 OpenAI",
      endpoint_url: "https://gateway.example/v1/responses",
      transport: "openai_responses",
      default_model: "team-model",
      models: ["team-model"],
    });
    const controls = createClient([existing]);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset} />
    </QueryClientProvider>);

    expect(await screen.findByRole("heading", { name: "编辑供应商" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("供应商 ID")).toBeDisabled();
    expect(screen.getByLabelText("显示名称")).toHaveValue("团队 OpenAI");
    expect(screen.getByLabelText("请求地址")).toHaveValue("https://gateway.example/v1/responses");
    expect(screen.getByLabelText("默认模型")).toHaveValue("team-model");
    await user.click(screen.getByRole("button", { name: "返回供应商列表" }));
    expect(await screen.findByRole("button", { name: /团队 OpenAI/u })).toBeInTheDocument();
  });

  it("keeps a preset API key in the OS credential path and exits only after a successful save", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    const onExit = vi.fn();
    const onSaved = vi.fn();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset}
        onExit={onExit} onSaved={onSaved} />
    </QueryClientProvider>);

    await screen.findByRole("heading", { name: "添加供应商" });
    await user.type(screen.getByLabelText("API Key"), "preset-one-time-key-123456");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(controls.changeProviderCredential).toHaveBeenCalledWith(
      "official-openai", {
        version: "provider_credential.v1", action: "set", confirm: true,
        secret: "preset-one-time-key-123456",
      },
    ));
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(onSaved.mock.calls[0][0].advanced_config).toEqual({
      request_body: { store: false },
      request_headers: {
        Authorization: { $credential: "official-openai", template: "Bearer ${secret}" },
      },
    });
    expect(JSON.stringify(controls.upsertProviderDefinition.mock.calls[0][1].definition))
      .not.toContain("preset-one-time-key-123456");
    expect(onExit).toHaveBeenCalledTimes(1);
  });

  it("saves and qualifies a first Provider once before returning its exact model to the draft", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    const onReady = vi.fn();
    render(<StrictMode><QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset}
        prepareForDraft onReady={onReady} />
    </QueryClientProvider></StrictMode>);

    await screen.findByRole("heading", { name: "添加供应商" });
    expect(screen.queryByLabelText("请求地址")).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "高级 JSON" })).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("默认模型"), "gpt-5-mini");
    await user.type(screen.getByLabelText("API Key"), "first-model-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));

    await waitFor(() => expect(onReady).toHaveBeenCalledWith(expect.objectContaining({
      id: "official-openai", default_model: "gpt-5-mini",
    })));
    expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1);
    expect(controls.changeProviderCredential).toHaveBeenCalledTimes(1);
    expect(controls.diagnoseProvider).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(1);
    expect(controls.qualifyModelHarness).toHaveBeenCalledWith({
      version: "model_harness_qualification.v1", provider: "official-openai",
      model: "gpt-5-mini", confirm_qualification: true,
    });
    expect(controls.availableModelRoutes).toHaveBeenCalledTimes(1);
  });

  it("reveals an invalid custom connection after collapsing advanced settings without losing it", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset} prepareForDraft />
    </QueryClientProvider>);
    await user.click(await screen.findByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    fireEvent.change(screen.getByLabelText("请求地址"), { target: { value: "http://remote.example/v1" } });
    await user.click(screen.getByRole("button", { name: "收起高级设置" }));
    await user.click(screen.getByRole("button", { name: "保存并检查" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("请求地址必须使用 HTTPS");
    expect(screen.getByLabelText("请求地址")).toHaveValue("http://remote.example/v1");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it("completes the first Provider check under the desktop StrictMode root", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    const onReady = vi.fn();
    render(<StrictMode><QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset}
        prepareForDraft onReady={onReady} />
    </QueryClientProvider></StrictMode>);

    await screen.findByRole("heading", { name: "添加供应商" });
    await user.type(screen.getByLabelText("API Key"), "first-model-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
    expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(1);
  });

  it("retries only the model check after a saved first Provider check fails", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    controls.qualifyModelHarness.mockRejectedValueOnce(new Error("temporary Provider failure"));
    const onReady = vi.fn();
    render(<StrictMode><QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset}
        prepareForDraft onReady={onReady} />
    </QueryClientProvider></StrictMode>);

    await screen.findByRole("heading", { name: "添加供应商" });
    await user.type(screen.getByLabelText("API Key"), "first-model-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("temporary Provider failure");
    await user.click(screen.getByRole("button", { name: "重新检查" }));

    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1);
    expect(controls.changeProviderCredential).toHaveBeenCalledTimes(1);
    expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(2);
  });

  it("keeps a saved Provider and reports billing capacity without automatic retry", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    const onReady = vi.fn();
    controls.qualifyModelHarness.mockResolvedValue({
      protocol_version: "model_harness_qualification.v1", provider: "official-openai", model: "gpt-5",
      status: "unreachable", outcome: "permanent", failure_reason: "capacity", retryable: false,
      network_request_attempted: true, model_calls: 1, synthetic_tool_calls: 0, tool_executed: false,
      response_content_returned: false, qualification_status: "capacity", duration_ms: 20,
      harness: { root_eligible: false },
    });
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset}
        prepareForDraft onReady={onReady} />
    </QueryClientProvider>);
    await screen.findByRole("heading", { name: "添加供应商" });
    await user.type(screen.getByLabelText("API Key"), "billing-test-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("供应商额度或容量不足，请检查账单或服务状态");
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-5");
    expect(screen.getByLabelText("API Key")).toHaveValue("");
    expect(screen.getByRole("button", { name: "重新检查" })).toBeEnabled();
    expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1);
    expect(controls.changeProviderCredential).toHaveBeenCalledTimes(1);
    expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(1);
    expect(controls.availableModelRoutes).not.toHaveBeenCalled();
    expect(onReady).not.toHaveBeenCalled();
  });

  it("locks the saved Provider form while the paid model check is in flight", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    let rejectCheck!: (reason?: unknown) => void;
    const pending = new Promise<never>((_resolve, reject) => { rejectCheck = reject; });
    controls.qualifyModelHarness.mockImplementationOnce(() => pending);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset}
        prepareForDraft onReady={vi.fn()} />
    </QueryClientProvider>);

    await screen.findByRole("heading", { name: "添加供应商" });
    await user.type(screen.getByLabelText("API Key"), "first-model-key-123456");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));
    await waitFor(() => expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(1));
    expect(screen.getByLabelText("默认模型")).toBeDisabled();
    expect(screen.getByLabelText("API Key")).toBeDisabled();
    expect(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "返回供应商列表" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "正在保存…" })).toBeDisabled();
    rejectCheck(new Error("bounded check stopped"));
    expect(await screen.findByRole("alert")).toHaveTextContent("bounded check stopped");
    expect(screen.getByLabelText("默认模型")).toBeEnabled();
    expect(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" })).toBeEnabled();
  });

  it("lists providers without redisplaying their stored API key and inserts a credential reference", async () => {
    const user = userEvent.setup();
    const controls = createClient([provider()]);
    renderSettings(controls.client);

    const row = await screen.findByRole("button", { name: /Acme AI/u });
    expect(within(row).getByText("密钥已存储")).toBeInTheDocument();
    await user.click(row);

    const keyInput = screen.getByLabelText("API Key");
    expect(keyInput).toHaveValue("");
    expect(keyInput).toHaveAttribute("type", "password");
    expect(keyInput).toHaveAttribute("placeholder", "留空以保留现有密钥");
    await user.click(screen.getByRole("button", { name: "插入凭据引用" }));
    expect(screen.getByRole("textbox", { name: "高级 JSON" })).toHaveValue(
      JSON.stringify({ request_headers: {
        Authorization: { $credential: "acme", template: "Bearer ${secret}" },
      } }, null, 2),
    );
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it("keeps Harness verification disabled until the exact provider configuration is saved", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);

    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    const verifyButton = screen.getByRole("button", { name: "测试并验证 Harness" });
    expect(verifyButton).toBeDisabled();
    expect(screen.getByText("先保存供应商配置后才能验证。")).toBeInTheDocument();
    expect(controls.diagnoseProvider).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it("does not infer Provider-hosted search from Responses transport and retains an explicit choice", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);

    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    expect(screen.getByLabelText("搜索策略")).toHaveValue("auto");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).not.toBeChecked();

    await user.selectOptions(screen.getByLabelText("协议"), "openai_responses");

    expect(screen.getByLabelText("搜索策略")).toHaveValue("auto");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).not.toBeChecked();
    expect(screen.getByText(/先确认供应商提供哪种搜索能力/u)).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("搜索策略"), "provider_native");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).toBeChecked();
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.diagnoseProvider).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();

    await user.selectOptions(screen.getByLabelText("协议"), "openai_chat_completions");
    expect(screen.getByLabelText("搜索策略")).toHaveValue("auto");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).not.toBeChecked();
  });

  it.each([
    ["https://api.deepseek.com/responses", false],
    ["https://API.DEEPSEEK.COM./responses", false],
    ["https://api.deepseek.com.proxy.example/responses", true],
    ["https://gateway.example/responses", true],
  ] as const)("uses exact endpoint support for a new preset at %s", async (endpointURL, declared) => {
    const controls = createClient();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ProviderSettings client={controls.client} initialPreset={{ ...openAIPreset,
        id: "official-deepseek", displayName: "DeepSeek", endpointURL }} />
    </QueryClientProvider>);
    await screen.findByRole("heading", { name: "添加供应商" });
    fireEvent.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("搜索策略")).toHaveValue(declared ? "provider_native" : "auto");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search").getAttribute("type")).toBe("checkbox");
    if (declared) expect(screen.getByLabelText("声明供应商具备原生 Web Search")).toBeChecked();
    else expect(screen.getByLabelText("声明供应商具备原生 Web Search")).not.toBeChecked();
    expect(screen.getByLabelText("请求地址")).toHaveValue(endpointURL);
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it("shows a stored official endpoint declaration without silently rewriting it from the preset", async () => {
    const existing = provider({ id: "official-deepseek", endpoint_url: "https://api.deepseek.com/responses",
      transport: "openai_responses", search_mode: "provider_native", native_web_search_capability: "declared_unverified" });
    const controls = createClient([existing]);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ProviderSettings client={controls.client} initialPreset={{ ...openAIPreset,
        id: "official-deepseek", endpointURL: existing.endpoint_url }} />
    </QueryClientProvider>);
    await screen.findByRole("heading", { name: "编辑供应商" });
    fireEvent.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("搜索策略")).toHaveValue("provider_native");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).toBeChecked();
    expect(screen.getByText(/需要更换搜索方式/u)).toBeInTheDocument();
    expect(screen.getByLabelText("默认模型")).toHaveValue(existing.default_model);
    expect(screen.getByLabelText("API Key")).toHaveValue("");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it("saves independent DuckDuckGo search without a native declaration or credential changes", async () => {
    const controls = createClient([provider()]);
    const user = userEvent.setup();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.selectOptions(screen.getByLabelText("搜索策略"), "web");
    expect(screen.getByRole("option", { name: "普通网页搜索（DuckDuckGo）" })).toBeInTheDocument();
    expect(screen.getByText(/搜索查询会发送到 DuckDuckGo/u)).toHaveTextContent("遵循当前任务的网页访问范围");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).not.toBeChecked();
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition).toMatchObject({
      search_mode: "web", native_web_search_capability: "unsupported", models: ["acme-pro"],
      endpoint_url: "https://api.acme.example/v1/chat/completions",
    });
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
    expect(controls.diagnoseProvider).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();
  });

  it("clears an inherited native claim when the operator changes to the known unsupported endpoint", async () => {
    const controls = createClient();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset} />
    </QueryClientProvider>);
    await screen.findByRole("heading", { name: "添加供应商" });
    fireEvent.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("搜索策略")).toHaveValue("provider_native");
    fireEvent.change(screen.getByLabelText("请求地址"), { target: { value: "https://api.deepseek.com/responses" } });
    expect(screen.getByLabelText("搜索策略")).toHaveValue("auto");
    expect(screen.getByLabelText("声明供应商具备原生 Web Search")).not.toBeChecked();
    expect(screen.getByRole("option", { name: "供应商原生" })).toBeDisabled();
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-5");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
  });

  it("requires billing confirmation, diagnoses first, then displays Harness qualification expiry", async () => {
    const user = userEvent.setup();
    const controls = createClient([provider()]);
    renderSettings(controls.client);

    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    const verifyButton = screen.getByRole("button", { name: "测试并验证 Harness" });
    expect(verifyButton).toBeEnabled();
    await user.click(verifyButton);

    const dialog = screen.getByRole("dialog", { name: "测试并验证 Harness？" });
    expect(dialog).toHaveTextContent("通常还需要两次模型调用");
    expect(dialog).toHaveTextContent("可能按这些调用计费");
    expect(controls.diagnoseProvider).not.toHaveBeenCalled();
    expect(controls.qualifyModelHarness).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole("button", { name: "开始验证" }));
    await waitFor(() => expect(controls.diagnoseProvider).toHaveBeenCalledWith({
      version: "provider_diagnostic.v1",
      provider: "acme",
      model: "acme-pro",
      confirm_diagnostic: true,
    }));
    await waitFor(() => expect(controls.qualifyModelHarness).toHaveBeenCalledWith({
      version: "model_harness_qualification.v1",
      provider: "acme",
      model: "acme-pro",
      confirm_qualification: true,
    }));
    expect(controls.diagnoseProvider.mock.invocationCallOrder[0])
      .toBeLessThan(controls.qualifyModelHarness.mock.invocationCallOrder[0]);

    const result = await screen.findByRole("status", { name: "Harness 验证结果" });
    expect(result).toHaveTextContent("验证通过");
    expect(result).toHaveTextContent("2 次");
    expect(result).toHaveTextContent("有效期至");
    expect(within(result).queryByText("未记录")).not.toBeInTheDocument();

    await user.clear(screen.getByLabelText("显示名称"));
    await user.type(screen.getByLabelText("显示名称"), "Acme Team");
    expect(screen.getByRole("button", { name: "测试并验证 Harness" })).toBeDisabled();
    expect(screen.getByText(/当前更改尚未保存/u)).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "Harness 验证结果" })).not.toBeInTheDocument();
  });

  it("requires a second confirmation, migrates exactly one plaintext secret, and never sends it in the definition", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    const plaintextSecret = "sk-live-super-secret-123456";
    fireEvent.change(screen.getByRole("textbox", { name: "高级 JSON" }), { target: { value:
      JSON.stringify({ request_headers: { Authorization: `Bearer ${plaintextSecret}` } }),
    } });

    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    const dialog = screen.getByRole("dialog", { name: "迁移明文密钥？" });
    expect(within(dialog).getByText(/系统凭据管理器/u)).toBeInTheDocument();
    expect(dialog).not.toHaveTextContent(plaintextSecret);

    await user.click(within(dialog).getByRole("button", { name: "迁移并保存" }));
    await waitFor(() => expect(controls.changeProviderCredential).toHaveBeenCalledWith("nova", {
      version: "provider_credential.v1", action: "set", confirm: true,
      secret: plaintextSecret,
    }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.invocationCallOrder[0])
      .toBeLessThan(controls.changeProviderCredential.mock.invocationCallOrder[0]);
    const definition = controls.upsertProviderDefinition.mock.calls[0][1].definition;
    expect(JSON.stringify(definition)).not.toContain(plaintextSecret);
    expect(definition.advanced_config).toEqual({
      request_headers: { Authorization: { $credential: "nova", template: "Bearer ${secret}" } },
    });
    expect(screen.queryByLabelText("API Key")).not.toBeInTheDocument();
    expect(await screen.findByText("Nova Cloud")).toBeInTheDocument();
  });

  it("blocks multi-secret JSON and keeps destructive deletion keyboard-dismissible", async () => {
    const user = userEvent.setup();
    const controls = createClient([provider()]);
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));

    fireEvent.change(screen.getByRole("textbox", { name: "高级 JSON" }), { target: { value:
      JSON.stringify({ api_key: "first-secret-value", password: "second-secret-value" }),
    } });
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("多个不同的明文密钥");
    expect(screen.queryByRole("dialog", { name: "迁移明文密钥？" })).not.toBeInTheDocument();
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();

    const deleteButton = screen.getByRole("button", { name: "删除供应商" });
    await user.click(deleteButton);
    expect(screen.getByRole("dialog", { name: "删除 Acme AI" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "删除 Acme AI" })).not.toBeInTheDocument();
    expect(deleteButton).toHaveFocus();
    expect(controls.deleteProviderDefinition).not.toHaveBeenCalled();
  });

  it("rejects duplicate advanced JSON keys before JSON.parse can collapse them", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    fireEvent.change(screen.getByRole("textbox", { name: "高级 JSON" }), {
      target: { value: '{"region":"one","r\\u0065gion":"two"}' },
    });
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("重复字段“region”");
    expect(controls.upsertProviderDefinition).not.toHaveBeenCalled();
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
  });

  it("stores one-time API keys separately and clears the editor after save", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    await user.type(screen.getByLabelText("API Key"), "one-time-key-123456");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(controls.changeProviderCredential).toHaveBeenCalledWith("nova", {
      version: "provider_credential.v1", action: "set", confirm: true,
      secret: "one-time-key-123456",
    }));
    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.invocationCallOrder[0])
      .toBeLessThan(controls.changeProviderCredential.mock.invocationCallOrder[0]);
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition)
      .not.toHaveProperty("api_key");
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.advanced_config)
      .toEqual({ request_headers: {
        Authorization: { $credential: "nova", template: "Bearer ${secret}" },
      } });
    expect(screen.queryByLabelText("API Key")).not.toBeInTheDocument();
  });

  it("lets the operator disable JSON reference synchronization", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    await user.click(screen.getByRole("checkbox", { name: /把凭据引用同步到高级 JSON/u }));
    await user.type(screen.getByLabelText("API Key"), "one-time-key-123456");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(controls.changeProviderCredential).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.advanced_config)
      .toEqual({});
  });

  it("synchronizes Anthropic credentials as x-api-key references", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    await user.selectOptions(screen.getByLabelText("协议"), "anthropic_messages");
    await user.type(screen.getByLabelText("API Key"), "anthropic-key-123456");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.advanced_config)
      .toEqual({ request_headers: {
        "x-api-key": { $credential: "nova" },
      } });
  });

  it("keeps a newly registered definition visible when its first credential write fails", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    controls.changeProviderCredential.mockRejectedValueOnce(new Error("credential store unavailable"));
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: "添加供应商" }));
    await fillRequiredProvider(user);
    await user.type(screen.getByLabelText("API Key"), "one-time-key-123456");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(controls.changeProviderCredential).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.invocationCallOrder[0])
      .toBeLessThan(controls.changeProviderCredential.mock.invocationCallOrder[0]);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "供应商定义已保存，但 API Key 未写入系统凭据",
    );
    expect(screen.getByRole("heading", { name: "编辑供应商" })).toBeInTheDocument();
    expect(screen.getByLabelText("供应商 ID")).toBeDisabled();
    expect(screen.getByLabelText("API Key")).toHaveValue("");
    expect(controls.deleteProviderDefinition).not.toHaveBeenCalled();
  });

  it("delegates definition and credential deletion to one revision-bound backend operation", async () => {
    const user = userEvent.setup();
    const controls = createClient([provider()]);
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "删除供应商" }));
    const dialog = screen.getByRole("dialog", { name: "删除 Acme AI" });
    expect(within(dialog).getByText(/原凭据会保留/u)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "删除" }));

    await waitFor(() => expect(controls.deleteProviderDefinition).toHaveBeenCalledWith("acme", {
      version: "provider_definition_control.v1", expected_collection_revision: 7,
      expected_definition_revision: 4, confirm: true,
    }));
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("heading", { name: "编辑供应商" })).not.toBeInTheDocument());
  });

  it.each(["供应商仍被路由引用", "供应商版本已过期", "系统凭据清理失败，原配置已恢复"])(
    "retains the editor and refreshes both inventories when deletion is rejected: %s", async (message) => {
    const user = userEvent.setup();
    const controls = createClient([provider()]);
    controls.deleteProviderDefinition.mockRejectedValueOnce(new Error(message));
    renderSettings(controls.client);
    await user.click(await screen.findByRole("button", { name: /Acme AI/u }));
    await user.click(screen.getByRole("button", { name: "删除供应商" }));
    await user.click(within(screen.getByRole("dialog", { name: "删除 Acme AI" }))
      .getByRole("button", { name: "删除" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(message);
    expect(controls.changeProviderCredential).not.toHaveBeenCalled();
    await waitFor(() => expect(controls.providerDefinitions).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(controls.providerCredentialStatuses).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("heading", { name: "编辑供应商" })).toBeInTheDocument();
  });

  it("streamlines first model onboarding with accessible primary actions and lossless on-demand advanced settings", async () => {
    const user = userEvent.setup();
    const controls = createClient();
    const onReady = vi.fn();
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <V2ProviderSettings client={controls.client} initialPreset={openAIPreset} onReady={onReady} prepareForDraft />
    </QueryClientProvider>);

    // 1. Verify default view only shows necessary fields and accessible primary action
    expect(await screen.findByRole("heading", { name: "添加供应商" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "OpenAI" })).toBeInTheDocument();
    expect(screen.getByLabelText("API Key")).toBeInTheDocument();
    expect(screen.getByLabelText("默认模型")).toHaveValue("gpt-5");
    expect(screen.getByRole("button", { name: "保存并检查" })).toBeInTheDocument();

    // Verify model catalog, output limits and advanced connection are collapsed by default
    expect(screen.queryByLabelText("请求地址")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "获取模型列表" })).not.toBeInTheDocument();
    expect(screen.queryByRole("spinbutton", { name: "gpt-5 默认输出 token" })).not.toBeInTheDocument();

    // 2. Expand advanced settings on demand, edit advanced configuration, and collapse back
    await user.click(screen.getByRole("button", { name: "高级设置：自定义连接、模型与搜索" }));
    expect(screen.getByLabelText("请求地址")).toHaveValue("https://api.openai.com/v1/responses");
    expect(screen.getByRole("spinbutton", { name: "gpt-5 默认输出 token" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("请求地址"), { target: { value: "https://custom-gateway.openai.com/v1/responses" } });

    await user.click(screen.getByRole("button", { name: "收起高级设置" }));
    expect(screen.queryByLabelText("请求地址")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存并检查" })).toBeInTheDocument();

    // 3. Fill API Key and complete save-and-check workflow
    fireEvent.change(screen.getByLabelText("API Key"), { target: { value: "sk-streamlined-test-key" } });
    await user.click(screen.getByRole("button", { name: "保存并检查" }));

    await waitFor(() => expect(controls.upsertProviderDefinition).toHaveBeenCalledTimes(1));
    expect(controls.upsertProviderDefinition.mock.calls[0][1].definition.endpoint_url).toBe("https://custom-gateway.openai.com/v1/responses");
    expect(controls.changeProviderCredential).toHaveBeenCalledWith("official-openai", expect.objectContaining({
      secret: "sk-streamlined-test-key",
    }));
    await waitFor(() => expect(controls.qualifyModelHarness).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(onReady).toHaveBeenCalledTimes(1));
  });
});
