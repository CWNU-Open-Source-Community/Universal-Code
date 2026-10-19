import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { peerMessageFixture } from "../../test/thread-peer-message";
import { V2PeerMessageCard } from "./thread-peer-messages";

afterEach(cleanup);

it("shows a peer source, links to the other chat and distinguishes delivery from completion", async () => {
  const open = vi.fn();
  const message = peerMessageFixture({ status: "delivered", delivered_at: "2026-10-19T09:00:01Z" });
  render(<V2PeerMessageCard message={message} threadID="thread-b" onOpenThread={open} />);
  const user = userEvent.setup();
  expect(screen.getByText("Delivered to model")).toBeVisible();
  expect(screen.queryByText("Completed")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "API migration" }));
  expect(open).toHaveBeenCalledExactlyOnceWith("thread-a");
});
