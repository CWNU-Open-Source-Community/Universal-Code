import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { ThreadView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { ThreadActions } from "./thread-actions";

const thread = { id: "thread-sidebar", title: "Review result" } as ThreadView;

function show(client: Partial<APIClient>) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidation = vi.spyOn(queryClient, "invalidateQueries");
  render(<QueryClientProvider client={queryClient}><LocaleProvider>
    <ThreadActions thread={thread} client={{ hasControl: true, ...client } as APIClient} onArchive={vi.fn()} />
  </LocaleProvider></QueryClientProvider>);
  return invalidation;
}

beforeEach(() => localStorage.setItem("prayu.locale.v1", "en-US"));

it("pins the exact chat and refreshes pinned history and current details", async () => {
  const user = userEvent.setup();
  const pinThread = vi.fn().mockResolvedValue({ ...thread, pinned: true });
  const invalidate = show({ pinThread });
  await user.click(screen.getByRole("button", { name: "Review result chat options" }));
  expect(screen.getByRole("menuitem", { name: "Pin chat" })).toHaveFocus();
  await user.click(screen.getByRole("menuitem", { name: "Pin chat" }));
  await waitFor(() => expect(pinThread).toHaveBeenCalledWith(thread.id, true));
  await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["v2", "threads"] }));
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["v2", "thread", thread.id] });
});

it("keeps a failed rename editable and saves a corrected title", async () => {
  const user = userEvent.setup();
  const renameThread = vi.fn().mockRejectedValueOnce(new Error("Save failed")).mockResolvedValueOnce({ ...thread, title: "Finished review" });
  show({ renameThread });
  await user.click(screen.getByRole("button", { name: "Review result chat options" }));
  await user.keyboard("{ArrowDown}{Enter}");
  const input = screen.getByRole("textbox", { name: "Chat name" });
  expect(input).toHaveFocus();
  await user.clear(input);
  await user.type(input, "Finished review");
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Save failed");
  expect(input).toHaveValue("Finished review");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(renameThread).toHaveBeenLastCalledWith(thread.id, "Finished review");
});
