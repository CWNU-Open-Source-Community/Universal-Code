import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { ModelAvailabilitySettings } from "./model-availability-dialog";

function mockHarness(model: string) {
  return {
    protocol_version: "model_harness.v1", model, transport_protocol: "mock",
    tool_strategy: "native", json_strategy: "native",
    qualification_status: "trusted_builtin", tool_calls_qualified: true,
    tool_results_qualified: true, strict_json_qualified: true,
    streaming_qualified: true, root_eligible: true,
    structured_json_eligible: true, qualified_at: "", expires_at: "",
  };
}

function requiredHarness(model: string) {
  return {
    ...mockHarness(model), transport_protocol: "anthropic_messages",
    json_strategy: "prompt", qualification_status: "qualification_required",
    tool_calls_qualified: false, tool_results_qualified: false,
    strict_json_qualified: false, streaming_qualified: false,
    root_eligible: false, structured_json_eligible: false,
  };
}

function openAIHarness(model: string) {
  return {
    ...requiredHarness(model), transport_protocol: "openai_chat_completions",
    json_strategy: "native",
  };
}

describe("ModelAvailabilitySettings", () => {
  it("renders redacted provider and route status without configuration secrets", async () => {
    const user = userEvent.setup();
    const client = { modelAvailability: vi.fn().mockResolvedValue({
      protocol_version: "model_availability.v2", generation: 1,
      providers: [{ name: "mock", kind: "local", status: "available", models: ["mock-code"],
        harnesses: [mockHarness("mock-code")],
        credential_source: "none", network_required: false, configuration_error: false }],
      routes: [{ name: "code", provider: "mock", model: "mock-code", available: true,
        harness_ready: true }],
    }) } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(<QueryClientProvider client={queryClient}>
      <ModelAvailabilitySettings client={client} />
    </QueryClientProvider>);
    expect(await screen.findByText("Default")).toBeVisible();
    expect(screen.getByText("mock-code")).not.toBeVisible();
    await user.click(screen.getByText("Per-model diagnostics"));
    expect(screen.getByText("mock-code")).toBeVisible();
    expect(container.textContent).not.toContain("api_key");
    expect(container.textContent).not.toContain("base_url");
  });

  it("keeps diagnostics and route selection behind explicit model control", async () => {
    const user = userEvent.setup();
    const diagnoseProvider = vi.fn().mockResolvedValue({
      protocol_version: "provider_diagnostic.v1", provider: "mock", model: "mock-code",
      status: "reachable", outcome: "success", failure_reason: "none", retryable: false,
      network_request_attempted: false, model_called: true, tool_called: false,
      response_content_returned: false, duration_ms: 2,
    });
    const selectModelRoute = vi.fn().mockResolvedValue({
      name: "code", provider: "mock", model: "mock-fast", available: true,
      harness_ready: true,
    });
    const client = {
      hasModelControl: true, diagnoseProvider, selectModelRoute,
      modelAvailability: vi.fn().mockResolvedValue({
        protocol_version: "model_availability.v2", generation: 1,
        providers: [{ name: "mock", kind: "local", status: "available",
          models: ["mock-code", "mock-fast"], credential_source: "none",
          network_required: false, configuration_error: false,
          harnesses: [mockHarness("mock-code"), mockHarness("mock-fast")] }],
        routes: [{ name: "code", provider: "mock", model: "mock-code", available: true,
          harness_ready: true }],
      }),
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    render(<QueryClientProvider client={queryClient}>
      <ModelAvailabilitySettings client={client} />
    </QueryClientProvider>);
    await user.click(await screen.findByText("Per-model diagnostics"));
    expect(screen.getByRole("button", { name: "Diagnose mock/mock-code" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Diagnose mock/mock-fast" }));
    await waitFor(() => expect(diagnoseProvider).toHaveBeenCalledWith({
      version: "provider_diagnostic.v1", provider: "mock", model: "mock-fast",
      confirm_diagnostic: true,
    }));
    expect(await screen.findByText("reachable")).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Default model for new conversations"), "mock/mock-fast");
    await user.click(screen.getByRole("button", { name: "Save default model for new conversations" }));
    await waitFor(() => expect(selectModelRoute).toHaveBeenCalledWith("code", {
      version: "model_route_control.v1", provider: "mock", model: "mock-fast",
    }));
  });

  it("keeps Harness qualification separate and explicitly confirmed", async () => {
    const user = userEvent.setup();
    const qualifyModelHarness = vi.fn().mockResolvedValue({
      protocol_version: "model_harness_qualification.v1", provider: "mimo",
      model: "model-secondary", status: "qualified", outcome: "success",
      failure_reason: "none", retryable: false,
      network_request_attempted: true, model_calls: 2, synthetic_tool_calls: 1,
      tool_executed: false, response_content_returned: false, duration_ms: 8,
      harness: { ...mockHarness("model-secondary"), transport_protocol: "anthropic_messages",
        json_strategy: "prompt", qualification_status: "verified",
        qualified_at: "2026-07-25T00:00:00Z", expires_at: "2026-08-01T00:00:00Z" },
    });
    const client = {
      hasModelControl: true, qualifyModelHarness,
      modelAvailability: vi.fn().mockResolvedValue({
        protocol_version: "model_availability.v2", generation: 1,
        providers: [{ name: "mimo", kind: "anthropic_compatible", status: "available",
          models: ["model-primary", "model-secondary"], credential_source: "system",
          network_required: true, configuration_error: false,
          harnesses: [mockHarness("model-primary"), requiredHarness("model-secondary")] }],
        routes: [{ name: "code", provider: "mimo", model: "model-secondary", available: true,
          harness_ready: false }],
      }),
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    render(<QueryClientProvider client={queryClient}>
      <ModelAvailabilitySettings client={client} />
    </QueryClientProvider>);
    await user.click(await screen.findByText("Per-model diagnostics"));
    expect(screen.getByRole("button", { name: "Qualify mimo/model-primary Harness" }))
      .toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Qualify mimo/model-secondary Harness" }));
    await waitFor(() => expect(qualifyModelHarness).toHaveBeenCalledWith({
      version: "model_harness_qualification.v1", provider: "mimo", model: "model-secondary",
      confirm_qualification: true,
    }));
    expect(await screen.findByText("2 model calls")).toBeInTheDocument();
  });

  it("renders OpenAI-compatible transport and safe connection failure reasons", async () => {
    const user = userEvent.setup();
    const diagnoseProvider = vi.fn().mockResolvedValue({
      protocol_version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "permanent",
      failure_reason: "authentication", retryable: false,
      network_request_attempted: true, model_called: true, tool_called: false,
      response_content_returned: false, duration_ms: 3,
    });
    const client = {
      hasModelControl: true, diagnoseProvider,
      modelAvailability: vi.fn().mockResolvedValue({
        protocol_version: "model_availability.v2", generation: 2,
        providers: [{ name: "openai", kind: "openai_compatible", status: "available",
          models: ["gpt-4.1-mini"], credential_source: "environment",
          network_required: true, configuration_error: false,
          harnesses: [openAIHarness("gpt-4.1-mini")] }],
        routes: [{ name: "code", provider: "openai", model: "gpt-4.1-mini",
          available: true, harness_ready: false }],
      }),
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    render(<QueryClientProvider client={queryClient}>
      <ModelAvailabilitySettings client={client} />
    </QueryClientProvider>);
    await user.click(await screen.findByText("Per-model diagnostics"));
    expect(screen.getByText("openai_chat_completions · JSON native")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Diagnose openai/gpt-4.1-mini" }));
    expect(await screen.findByText("authentication failed")).toBeInTheDocument();
  });

  it("allows a content-free diagnostic for a known unconfigured Provider", async () => {
    const user = userEvent.setup();
    const diagnoseProvider = vi.fn().mockResolvedValue({
      protocol_version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "permanent",
      failure_reason: "not_configured", retryable: false,
      network_request_attempted: false, model_called: false, tool_called: false,
      response_content_returned: false, duration_ms: 0,
    });
    const qualifyModelHarness = vi.fn().mockResolvedValue({
      protocol_version: "model_harness_qualification.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "permanent",
      failure_reason: "not_configured", retryable: false,
      network_request_attempted: false, model_calls: 0, synthetic_tool_calls: 0,
      tool_executed: false, response_content_returned: false, duration_ms: 0,
      harness: openAIHarness("gpt-4.1-mini"),
    });
    const client = {
      hasModelControl: true, diagnoseProvider, qualifyModelHarness,
      modelAvailability: vi.fn().mockResolvedValue({
        protocol_version: "model_availability.v2", generation: 2,
        providers: [{ name: "openai", kind: "openai_compatible",
          status: "not_configured", models: ["gpt-4.1-mini"],
          credential_source: "none", network_required: true,
          configuration_error: false, harnesses: [openAIHarness("gpt-4.1-mini")] }],
        routes: [{ name: "code", provider: "mock", model: "mock-code",
          available: true, harness_ready: true }],
      }),
    } as unknown as APIClient;
    const queryClient = new QueryClient({ defaultOptions: {
      queries: { retry: false }, mutations: { retry: false },
    } });
    render(<QueryClientProvider client={queryClient}>
      <ModelAvailabilitySettings client={client} />
    </QueryClientProvider>);
    await user.click(await screen.findByText("Per-model diagnostics"));
    await user.click(screen.getByRole("button", {
      name: "Diagnose openai/gpt-4.1-mini",
    }));
    expect((await screen.findAllByText("not configured")).length).toBe(2);
    expect(diagnoseProvider).toHaveBeenCalledWith({ version: "provider_diagnostic.v1",
      provider: "openai", model: "gpt-4.1-mini", confirm_diagnostic: true });
    await user.click(screen.getByRole("button", {
      name: "Qualify openai/gpt-4.1-mini Harness",
    }));
    await waitFor(() => expect(qualifyModelHarness).toHaveBeenCalledWith({
      version: "model_harness_qualification.v1", provider: "openai",
      model: "gpt-4.1-mini", confirm_qualification: true,
    }));
  });
});
