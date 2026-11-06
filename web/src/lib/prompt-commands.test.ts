import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPromptCommand, deletePromptCommand, expandPromptCommand,
  getSafeLocalStorage, maximumPromptCommandCount, promptCommandsStorageKey, promptCommandIssueText,
  PromptCommandError, readPromptCommands, reservedPromptCommandNames,
  sortedPromptCommands, updatePromptCommand } from "./prompt-commands";

let backing: Record<string, string>;

function storage(): Storage {
  return {
    getItem: (key: string) => key in backing ? backing[key] : null,
    setItem: (key: string, value: string) => { backing[key] = value; },
    removeItem: (key: string) => { delete backing[key]; },
    clear: () => { backing = {}; },
    key: (index: number) => Object.keys(backing)[index] ?? null,
    get length() { return Object.keys(backing).length; },
  };
}

function failStorage(): Storage {
  return {
    getItem: () => { throw new Error("quota"); },
    setItem: () => { throw new Error("quota"); },
    removeItem: () => { throw new Error("quota"); },
    clear: () => {},
    key: () => null,
    length: 0,
  };
}

const input = { name: "release-check", title: "Release checklist", body: "Check the release notes for $ARGUMENTS." };

beforeEach(() => { backing = {}; });
afterEach(() => vi.restoreAllMocks());

describe("prompt command library", () => {
  it("creates, reads, updates and deletes commands in name order", () => {
    const store = storage();
    const first = createPromptCommand(store, input);
    const second = createPromptCommand(store, { name: "audit", title: "Audit", body: "Audit the module." });
    expect(second.created_at).toBeTypeOf("string");

    const read = readPromptCommands(store);
    expect(sortedPromptCommands(read).map((command) => command.name)).toEqual(["audit", "release-check"]);
    expect(read.commands).toHaveLength(2);

    const updated = updatePromptCommand(store, first.id, { name: "release-check",
      title: "Release checklist v2", body: "Check the notes again." });
    expect(updated.title).toBe("Release checklist v2");
    expect(updated.updated_at >= updated.created_at).toBe(true);

    deletePromptCommand(store, second.id);
    expect(readPromptCommands(store).commands.map((command) => command.id)).toEqual([first.id]);
  });

  it("rejects reserved, malformed, duplicate and oversized input without writing", () => {
    const store = storage();
    createPromptCommand(store, input);
    const before = readPromptCommands(store);

    for (const name of [...reservedPromptCommandNames, "Help", "1bad", "has space", "a".repeat(33), ""]) {
      expect(() => createPromptCommand(store, { ...input, name })).toThrow(PromptCommandError);
    }
    expect(() => createPromptCommand(store, { ...input, name: "other", title: "x".repeat(129) }))
      .toThrow(promptCommandIssueText("invalid_title"));
    expect(() => createPromptCommand(store, { ...input, name: "other", body: "" }))
      .toThrow(promptCommandIssueText("invalid_body"));
    expect(() => createPromptCommand(store, { ...input, name: "other",
      body: "x".repeat(16 * 1024 + 1) })).toThrow(promptCommandIssueText("invalid_body"));
    expect(() => createPromptCommand(store, input)).toThrow(promptCommandIssueText("duplicate_name"));
    expect(() => updatePromptCommand(store, "missing-id", input)).toThrow(promptCommandIssueText("not_found"));
    expect(readPromptCommands(store)).toEqual(before);
  });

  it("survives quota failures by reporting storage_unavailable without losing data", () => {
    const healthy = storage();
    const created = createPromptCommand(healthy, input);
    const broken = failStorage();
    expect(() => createPromptCommand(broken, { name: "audit", title: "Audit", body: "Body." }))
      .toThrow(promptCommandIssueText("storage_unavailable"));
    expect(() => deletePromptCommand(broken, created.id)).toThrow(PromptCommandError);
    // The healthy store keeps its content; the failing write never touched it.
    expect(readPromptCommands(healthy).commands).toHaveLength(1);
  });

  it("resets a corrupt store to empty instead of throwing on every read", () => {
    const store = storage();
    store.setItem(promptCommandsStorageKey, "{\"version\":1,\"commands\":[{\"id\":\"x\"}]}");
    expect(readPromptCommands(store)).toEqual({ version: 1, commands: [] });
    expect(store.getItem(promptCommandsStorageKey)).toBeNull();
    store.setItem(promptCommandsStorageKey, "not-json");
    expect(readPromptCommands(store).commands).toEqual([]);
  });

  it("drops duplicate-name entries from a hand-edited store instead of trusting them", () => {
    const store = storage();
    const now = new Date().toISOString();
    store.setItem(promptCommandsStorageKey, JSON.stringify({ version: 1, commands: [
      { id: "a", name: "dup", title: "A", body: "One.", created_at: now, updated_at: now },
      { id: "b", name: "dup", title: "B", body: "Two.", created_at: now, updated_at: now },
    ] }));
    expect(readPromptCommands(store).commands).toEqual([]);
  });

  it("enforces the library size limit", () => {
    const store = storage();
    for (let index = 0; index < maximumPromptCommandCount; index++) {
      createPromptCommand(store, { name: `cmd-${index}`, title: `Command ${index}`, body: "Body." });
    }
    expect(() => createPromptCommand(store, { name: "overflow", title: "Over", body: "Body." }))
      .toThrow(promptCommandIssueText("store_limit"));
  });

  it("expands $ARGUMENTS in place and appends unmarked bodies", () => {
    expect(expandPromptCommand("Review $ARGUMENTS carefully.", "src/main.ts"))
      .toBe("Review src/main.ts carefully.");
    expect(expandPromptCommand("Review $ARGUMENTS and $ARGUMENTS twice.", "it"))
      .toBe("Review it and it twice.");
    expect(expandPromptCommand("Plain body.", "extra context")).toBe("Plain body.\n\nextra context");
    expect(expandPromptCommand("Plain body.", "")).toBe("Plain body.");
    expect(expandPromptCommand("Needs $ARGUMENTS here.", "")).toBe("Needs  here.");
    // Operator input containing replacement patterns must survive verbatim.
    expect(expandPromptCommand("Review $ARGUMENTS.", "a $& b $$ c")).toBe("Review a $& b $$ c.");
  });

  it("safely retrieves localStorage or returns undefined when inaccessible", () => {
    expect(getSafeLocalStorage()).toBe(window.localStorage);
    const spy = vi.spyOn(window, "localStorage", "get").mockImplementation(() => {
      throw new DOMException("The operation is insecure.", "SecurityError");
    });
    try {
      expect(getSafeLocalStorage()).toBeUndefined();
    } finally {
      spy.mockRestore();
    }
  });
});
