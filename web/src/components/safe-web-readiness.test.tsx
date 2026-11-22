import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import { SafeWebReadinessPanel } from "./safe-web-readiness";

function mount(safeWebReadiness: ReturnType<typeof vi.fn>) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const client = { safeWebReadiness } as unknown as APIClient;
  render(<QueryClientProvider client={queryClient}><SafeWebReadinessPanel client={client} /></QueryClientProvider>);
}

it("rechecks blocked evidence and displays readiness only after the service confirms it", async () => {
  const user = userEvent.setup();
  const read = vi.fn().mockResolvedValueOnce({ ready: false, blocking_reason: "review_missing" })
    .mockResolvedValueOnce({ ready: true });
  mount(read);
  expect(await screen.findByText(/Browser isolation evidence needs review/)).toBeInTheDocument();
  expect(screen.getByText(/Blocking reason/)).toHaveTextContent("review_missing");
  expect(screen.queryByText(/Web isolation checks passed/)).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Check web environment again" }));
  expect(await screen.findByText(/Web isolation checks passed/)).toBeInTheDocument();
  expect(read).toHaveBeenCalledTimes(2);
  expect(read).toHaveBeenLastCalledWith("chrome", expect.any(AbortSignal));
});

it("offers a read-only retry when service diagnostics fail", async () => {
  const user = userEvent.setup();
  const read = vi.fn().mockRejectedValueOnce(new Error("service offline"))
    .mockResolvedValueOnce({ ready: false, blocking_reason: "evidence_expired" });
  mount(read);
  await screen.findByText("service offline");
  await user.click(screen.getByRole("button", { name: "Check web environment again" }));
  await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
  expect(await screen.findByText(/Blocking reason/)).toHaveTextContent("evidence_expired");
  expect(screen.getByText(/Collect and review new isolation evidence/)).toBeInTheDocument();
});
