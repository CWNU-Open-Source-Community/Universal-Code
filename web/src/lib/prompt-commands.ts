// Operator-owned reusable prompt commands: the Universal-Code counterpart of
// Claude Code's `.claude/commands` and Codex's custom prompts. They are stored
// client-side only (never server authority), and selecting one inserts its
// body into the composer as an ordinary operator draft — the operator still
// reads and edits the text before sending, so a stored command grants nothing
// and executes nothing by itself.

export const promptCommandsStorageKey = "universal-code.prompt-commands.v1";
export const promptCommandsSchemaVersion = 1;

export function getSafeLocalStorage(): Storage | undefined {
  try {
    return typeof window === "undefined" ? undefined : window.localStorage;
  } catch {
    return undefined;
  }
}

export const maximumPromptCommandCount = 64;
export const maximumPromptCommandBodyBytes = 16 * 1024;

// Reserved by the session slash-command dispatcher; a stored command must not
// shadow them.
export const reservedPromptCommandNames = new Set([
  "help", "compact", "model", "workspace", "ls", "read", "write", "run",
]);

export interface PromptCommand {
  id: string;
  name: string;
  title: string;
  body: string;
  created_at: string;
  updated_at: string;
}

export interface PromptCommandStore {
  version: typeof promptCommandsSchemaVersion;
  commands: PromptCommand[];
}

export type PromptCommandIssue =
  | "invalid_name"
  | "reserved_name"
  | "duplicate_name"
  | "invalid_title"
  | "invalid_body"
  | "store_limit"
  | "not_found"
  | "storage_unavailable"
  | "storage_corrupt";

const promptCommandNamePattern = /^[a-z][a-z0-9-]{0,31}$/u;

export function promptCommandIssueText(issue: PromptCommandIssue, detail = ""): string {
  switch (issue) {
    case "invalid_name": return `指令名只能用小写字母、数字和连字符（1~32 位，以字母开头）。${detail}`;
    case "reserved_name": return `指令名与内置会话命令冲突，请换一个名字。${detail}`;
    case "duplicate_name": return `已存在同名指令。${detail}`;
    case "invalid_title": return "标题不能超过 128 个字符。";
    case "invalid_body": return "指令内容不能超过 16 KiB。";
    case "store_limit": return `指令库最多保存 ${maximumPromptCommandCount} 条，请先删除不需要的指令。`;
    case "not_found": return "该指令已被删除或不存在。";
    case "storage_unavailable": return "本机存储不可用，指令库无法保存。";
    case "storage_corrupt": return "本机指令库数据无法解析，已重置为空。";
  }
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validName(value: unknown): value is string {
  return typeof value === "string" && promptCommandNamePattern.test(value);
}

function validTimestamp(value: unknown): value is string {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

function parseCommand(value: unknown): PromptCommand | null {
  if (!isRecord(value) || typeof value.id !== "string" || value.id.length === 0 || value.id.length > 64 ||
    !validName(value.name) || typeof value.title !== "string" || value.title.length > 128 ||
    typeof value.body !== "string" || byteLength(value.body) > maximumPromptCommandBodyBytes ||
    !validTimestamp(value.created_at) || !validTimestamp(value.updated_at)) {
    return null;
  }
  return { id: value.id, name: value.name, title: value.title, body: value.body,
    created_at: value.created_at, updated_at: value.updated_at };
}

function parseStore(raw: string): PromptCommandStore | null {
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!isRecord(value) || value.version !== promptCommandsSchemaVersion || !Array.isArray(value.commands)) {
    return null;
  }
  const commands: PromptCommand[] = [];
  const seen = new Set<string>();
  for (const entry of value.commands) {
    const command = parseCommand(entry);
    if (!command || seen.has(command.name)) return null;
    seen.add(command.name);
    commands.push(command);
  }
  return { version: promptCommandsSchemaVersion, commands };
}

function emptyStore(): PromptCommandStore {
  return { version: promptCommandsSchemaVersion, commands: [] };
}

function readStore(storage: Storage): PromptCommandStore | null {
  let raw: string | null;
  try {
    raw = storage.getItem(promptCommandsStorageKey);
  } catch {
    return null;
  }
  if (raw === null) return emptyStore();
  const parsed = parseStore(raw);
  if (parsed === null) {
    // A corrupt store resets to empty rather than failing every later write.
    try { storage.removeItem(promptCommandsStorageKey); } catch { /* ignore */ }
    return emptyStore();
  }
  return parsed;
}

function readStoreOrThrow(storage: Storage): PromptCommandStore {
  const store = readStore(storage);
  if (store === null) throw promptCommandError("storage_unavailable");
  return store;
}

function writeStore(storage: Storage, store: PromptCommandStore): void {
  try {
    storage.setItem(promptCommandsStorageKey, JSON.stringify(store));
  } catch {
    throw promptCommandError("storage_unavailable");
  }
}

export class PromptCommandError extends Error {
  readonly issue: PromptCommandIssue;

  constructor(issue: PromptCommandIssue) {
    super(promptCommandIssueText(issue));
    this.issue = issue;
  }
}

function promptCommandError(issue: PromptCommandIssue): PromptCommandError {
  return new PromptCommandError(issue);
}

export function sortedPromptCommands(store: PromptCommandStore): PromptCommand[] {
  return [...store.commands].sort((left, right) => left.name.localeCompare(right.name));
}

export function readPromptCommands(storage: Storage | undefined): PromptCommandStore {
  if (!storage) return emptyStore();
  return readStore(storage) ?? emptyStore();
}

export interface PromptCommandInput {
  name: string;
  title: string;
  body: string;
}

function validateInput(store: PromptCommandStore, input: PromptCommandInput,
  excludeID?: string,
): void {
  if (!validName(input.name)) {
    throw promptCommandError("invalid_name");
  }
  if (reservedPromptCommandNames.has(input.name)) throw promptCommandError("reserved_name");
  if (store.commands.some((command) => command.name === input.name && command.id !== excludeID)) {
    throw promptCommandError("duplicate_name");
  }
  if (input.title.length > 128) throw promptCommandError("invalid_title");
  if (byteLength(input.body) === 0 || byteLength(input.body) > maximumPromptCommandBodyBytes) {
    throw promptCommandError("invalid_body");
  }
}

function newPromptCommandID(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `cmd-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export function createPromptCommand(storage: Storage | undefined,
  input: PromptCommandInput,
): PromptCommand {
  if (!storage) throw promptCommandError("storage_unavailable");
  const store = readStoreOrThrow(storage);
  validateInput(store, input);
  if (store.commands.length >= maximumPromptCommandCount) throw promptCommandError("store_limit");
  const now = new Date().toISOString();
  const command: PromptCommand = { id: newPromptCommandID(), name: input.name,
    title: input.title, body: input.body, created_at: now, updated_at: now };
  writeStore(storage, { version: promptCommandsSchemaVersion, commands: [...store.commands, command] });
  return command;
}

export function updatePromptCommand(storage: Storage | undefined, id: string,
  input: PromptCommandInput,
): PromptCommand {
  if (!storage) throw promptCommandError("storage_unavailable");
  const store = readStoreOrThrow(storage);
  const existing = store.commands.find((command) => command.id === id);
  if (!existing) throw promptCommandError("not_found");
  validateInput(store, input, id);
  const updated: PromptCommand = { ...existing, name: input.name, title: input.title,
    body: input.body, updated_at: new Date().toISOString() };
  writeStore(storage, { version: promptCommandsSchemaVersion,
    commands: store.commands.map((command) => command.id === id ? updated : command) });
  return updated;
}

export function deletePromptCommand(storage: Storage | undefined, id: string): void {
  if (!storage) throw promptCommandError("storage_unavailable");
  const store = readStoreOrThrow(storage);
  if (!store.commands.some((command) => command.id === id)) throw promptCommandError("not_found");
  writeStore(storage, { version: promptCommandsSchemaVersion,
    commands: store.commands.filter((command) => command.id !== id) });
}

// $ARGUMENTS in the body receives whatever the operator typed after the
// command name; a body without the placeholder receives the arguments as an
// appended paragraph, so neither path silently drops operator input.
export function expandPromptCommand(body: string, args: string): string {
  if (body.includes("$ARGUMENTS")) return body.replaceAll("$ARGUMENTS", () => args);
  return args ? `${body}\n\n${args}` : body;
}
