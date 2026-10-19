import { createRef, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { APIClient } from "../../api/client";
import { peerMessageFixture } from "../../test/thread-peer-message";
import { V2PeerMessageCard, V2PeerMessageDialog } from "./thread-peer-messages";

afterEach(cleanup);

function setup(reply = false) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const send = vi.fn().mockRejectedValueOnce(new Error("Connection lost")).mockResolvedValue({ message: peerMessageFixture(), replayed: true });
  const client = { hasThreadControl: true, sendThreadPeerMessage: send,
    threadPeerTargets: vi.fn().mockResolvedValue({ items: [{ id: "thread-a", title: "API migration", status: "active", workspace_id: "ws" }], has_more: false }) } as unknown as APIClient;
  const onClose = vi.fn();
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  render(<V2PeerMessageDialog client={client} threadID="thread-b" sourceTitle="Frontend integration"
    reply={reply ? peerMessageFixture() : undefined} onClose={onClose} returnFocusRef={createRef()} />, { wrapper });
  return { client, send, onClose, queryClient };
}

it("retains the draft and idempotency key after an uncertain send, then closes only on confirmation", async () => {
  const fixture = setup();
  const user = userEvent.setup();
  await screen.findByRole("option", { name: "API migration" });
  await user.selectOptions(screen.getByLabelText("To"), "thread-a");
  await user.type(screen.getByLabelText("Message"), "Ready for integration");
  await user.click(screen.getByRole("button", { name: "Send message" }));
  await screen.findByRole("alert");
  expect(fixture.onClose).not.toHaveBeenCalled();
  expect(screen.getByLabelText("Message")).toHaveValue("Ready for integration");
  await user.click(screen.getByRole("button", { name: "Send message" }));
  await waitFor(() => expect(fixture.onClose).toHaveBeenCalledTimes(1));
  expect(fixture.send.mock.calls[0]).toEqual(fixture.send.mock.calls[1]);
  expect(fixture.send.mock.calls[0]).toEqual(["thread-b", { version: "thread_peer_message.v1", target_thread_id: "thread-a",
    content: "Ready for integration" }, expect.stringMatching(/^peer-message-/u)]);
  fixture.queryClient.clear();
});

it("keeps replies addressed to the source and attaches the original message identity", async () => {
  const fixture = setup(true);
  const user = userEvent.setup();
  await screen.findByRole("option", { name: "API migration" });
  expect(screen.getByLabelText("To")).toBeDisabled();
  expect(screen.getByLabelText("To")).toHaveValue("thread-a");
  await user.type(screen.getByLabelText("Message"), "Confirmed");
  await user.click(screen.getByRole("button", { name: "Send message" }));
  await screen.findByRole("alert");
  expect(fixture.send.mock.calls[0][1]).toMatchObject({ target_thread_id: "thread-a", reply_to: "peer-message-1" });
  fixture.queryClient.clear();
});

it("shows a peer source, links to the other chat and distinguishes delivery from completion", async () => {
  const open = vi.fn();
  const reply = vi.fn();
  const message = peerMessageFixture({ status: "delivered", delivered_at: "2026-10-19T09:00:01Z" });
  render(<V2PeerMessageCard message={message} threadID="thread-b" onOpenThread={open} onReply={reply} />);
  const user = userEvent.setup();
  expect(screen.getByText("Delivered to model")).toBeVisible();
  expect(screen.queryByText("Completed")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "API migration" }));
  expect(open).toHaveBeenCalledExactlyOnceWith("thread-a");
  await user.click(screen.getByRole("button", { name: "Reply" }));
  expect(reply).toHaveBeenCalledWith(message, expect.any(HTMLElement));
});
