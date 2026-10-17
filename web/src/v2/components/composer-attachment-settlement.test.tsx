import { File as NodeFile } from "node:buffer";
import { webcrypto } from "node:crypto";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import { fileAttachmentIdentities, type WorkspaceFileAttachment } from "../../api/file-attachments";
import { V2RecoveryProvider } from "../recovery-storage";
import { useV2ThreadTurn, type V2TurnInput } from "../use-thread-turn";
import { V2Composer } from "./composer";

const workspaceID = "workspace-settlement";
const threadID = "thread-settlement";
const scopeID = `ds1_${"e".repeat(64)}`;

async function receipt(file: File): Promise<WorkspaceFileAttachment> {
  const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", await file.arrayBuffer()))]
    .map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return { id: `file-${sha256.slice(0, 16)}`, workspace_id: workspaceID, name: file.name,
    sha256, byte_size: file.size, mime_type: file.type, readability: "stored_only", text_bytes: 0, redacted: false };
}

function fixture() {
  let accept!: () => void;
  const response = new Promise<unknown>((resolve) => { accept = () => resolve({}); });
  const client = { baseURL: "/api/v1", hasThreadControl: true,
    uploadWorkspaceFile: vi.fn(async (_workspace: string, file: File, _key: string) => receipt(file)),
    submitThreadTurn: vi.fn(() => response),
  };
  const recorded: V2TurnInput[] = [];
  function Harness() {
    const turn = useV2ThreadTurn(client as unknown as APIClient);
    return <V2Composer client={client as unknown as APIClient} threadID={threadID}
      workspaceID={workspaceID} workspaces={[]} onWorkspaceChange={() => {}}
      onSubmit={async (content, files, images, draftVersion, attachments) => {
        const input: V2TurnInput = { threadID, workspaceID, content, files, images, attachments, draftVersion,
          operationKey: `turn-${crypto.randomUUID()}`, createdAt: new Date().toISOString() };
        recorded.push(input);
        await turn.mutateAsync(input);
      }} />;
  }
  const mount = () => {
    const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const page = render(<QueryClientProvider client={queries}><V2RecoveryProvider client={client} scopeID={scopeID}>
      <Harness />
    </V2RecoveryProvider></QueryClientProvider>);
    return { ...page, queries };
  };
  return { client, recorded, accept, mount };
}

const archive = () => new File([new Uint8Array([80, 75, 3, 4, 0, 1, 2, 3])], "archive.zip", { type: "application/zip" });
const choose = (file: File) => fireEvent.change(screen.getByLabelText("选择图片或文件"), { target: { files: [file] } });
const input = () => screen.getByRole("textbox", { name: "继续对话" });

beforeEach(() => { localStorage.clear(); vi.stubGlobal("File", NodeFile); vi.stubGlobal("crypto", webcrypto); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it.each(["COMPOSER_ZIP_ACTUAL 请读取原ZIP", ""])("consumes the accepted complete draft including a ZIP and stays empty after remount (text: %s)", async (text) => {
  const user = userEvent.setup(); const test = fixture(); const page = test.mount();
  const file = archive(); const expected = await receipt(file);
  choose(file);
  await screen.findByRole("button", { name: "移除文件 archive.zip" });
  if (text) fireEvent.change(input(), { target: { value: text } });
  await waitFor(() => expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(test.client.submitThreadTurn).toHaveBeenCalledOnce());
  expect(test.recorded[0].draftVersion).toBeDefined();
  expect(test.recorded[0].attachments).toEqual([expected]);
  expect(test.client.submitThreadTurn).toHaveBeenCalledWith(threadID, {
    version: "thread_message_submission.v1", content: text, attachments: fileAttachmentIdentities([expected]),
  }, test.recorded[0].operationKey);
  expect(screen.getByRole("button", { name: "移除文件 archive.zip" })).toBeDisabled();

  await act(async () => test.accept());
  await waitFor(() => expect(input()).toHaveValue(""));
  await waitFor(() => expect(screen.queryByRole("button", { name: "移除文件 archive.zip" })).not.toBeInTheDocument());
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  page.unmount(); page.queries.clear();
  const reopened = test.mount();
  expect(input()).toHaveValue("");
  expect(screen.queryByRole("button", { name: "移除文件 archive.zip" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "发送消息" })).toBeDisabled();
  expect(test.client.submitThreadTurn).toHaveBeenCalledOnce();
  reopened.unmount(); reopened.queries.clear();
});

it("preserves a later complete version, including its inherited ZIP and new file, when the old send completes", async () => {
  const user = userEvent.setup(); const test = fixture(); const page = test.mount();
  choose(archive());
  await screen.findByRole("button", { name: "移除文件 archive.zip" });
  fireEvent.change(input(), { target: { value: "原ZIP请求" } });
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(test.client.submitThreadTurn).toHaveBeenCalledOnce());
  fireEvent.change(input(), { target: { value: "等待原回复时新写的要求" } });
  choose(new File(["new requirements"], "later.txt", { type: "text/plain" }));
  await screen.findByRole("button", { name: "移除文件 later.txt" });

  await act(async () => test.accept());
  await waitFor(() => expect(screen.getByRole("button", { name: "移除文件 archive.zip" })).toBeEnabled());
  expect(input()).toHaveValue("等待原回复时新写的要求");
  expect(screen.getByRole("button", { name: "移除文件 later.txt" })).toBeEnabled();
  page.unmount(); page.queries.clear();
  const reopened = test.mount();
  expect(input()).toHaveValue("等待原回复时新写的要求");
  expect(screen.getByRole("button", { name: "移除文件 archive.zip" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "移除文件 later.txt" })).toBeEnabled();
  expect(test.client.submitThreadTurn).toHaveBeenCalledOnce();
  reopened.unmount(); reopened.queries.clear();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));
