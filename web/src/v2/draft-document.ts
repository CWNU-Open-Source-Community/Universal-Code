import { validImageAttachments, type WorkspaceImageAttachment } from "../api/image-attachments";
import { validFileAttachments, type WorkspaceFileAttachment } from "../api/file-attachments";
import type { V2FileReference } from "./components/file-context";
import type { V2RecoveryStore } from "./recovery-storage";
import { validMCPReferences, type MCPDraftReference } from "../api/mcp-references";

export interface DraftSnapshot { text: string; files: V2FileReference[]; images: WorkspaceImageAttachment[]; attachments?: WorkspaceFileAttachment[]; mcpReferences?: MCPDraftReference[] }
// Older saved drafts have no attachments field. Treat its absence as an empty list.
export const draftSnapshotFingerprint = (value: DraftSnapshot) =>
  JSON.stringify([value.text, value.files, value.images, value.attachments ?? [], value.mcpReferences ?? []]);
export interface DraftRef { branchID: string; seq: number }
export interface DraftScope { key: string; workspaceID: string }
export interface DraftHead { ref: DraftRef; snapshot: DraftSnapshot; ownerID: string }
export interface DraftState {
  snapshot: DraftSnapshot;
  ref: DraftRef | null;
  heads: DraftHead[];
  conflict: boolean;
  headToken: string;
  error: string | null;
  persisted: boolean;
}
export type DraftUpdate = Partial<DraftSnapshot> | ((previous: DraftSnapshot) => DraftSnapshot);

interface Branch {
  version: "v2_draft_document.v1";
  scope: DraftScope;
  branchID: string;
  ownerID: string;
  seq: number;
  // A causal vector, not just direct parents. An old window's next sequence is
  // not covered by a resolution that only observed its previous sequence.
  bases: Record<string, number>;
  snapshot: DraftSnapshot;
}
export interface DraftCapture {
  readonly scope: DraftScope;
  readonly ref: DraftRef | null;
  readonly branchID: string;
  readonly ownerID: string;
  readonly snapshot: DraftSnapshot;
  readonly bases: Readonly<Record<string, number>>;
}
interface Session {
  scope: DraftScope;
  legacy: DraftSnapshot;
  records: Map<string, Branch>;
  versions: Map<string, Branch>;
  unsaved: Map<string, string>;
  selected: DraftRef | null;
  epoch: { branchID: string; anchor: DraftRef | null } | null;
  error: string | null;
}

const VERSION = "v2_draft_document.v1" as const;
const INVALID = "草稿版本记录异常；原记录已保留，未覆盖其他窗口的内容。";
const WRITE_FAILED = "这版草稿尚未保存到本机；内容仍在本窗口，刷新或退出可能丢失。";
const CHANGED = "其他窗口的草稿版本已经变化，请重新查看后选择。";
const MEMORY_ONLY = "当前连接无法持久保存草稿；内容仅保留在本窗口。";
const RECENT_UI_VERSIONS = 8;
const empty = (): DraftSnapshot => ({ text: "", files: [], images: [] });
const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const id = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
const seq = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) > 0;
const refKey = (ref: DraftRef) => JSON.stringify([ref.branchID, ref.seq]);
const sameRef = (left: DraftRef | null, right: DraftRef | null) => left === null || right === null
  ? left === right : left.branchID === right.branchID && left.seq === right.seq;
const branchRef = ({ branchID, seq: number }: Branch): DraftRef => ({ branchID, seq: number });
const prefix = (scope: DraftScope) => `draft-document:${JSON.stringify([scope.key, scope.workspaceID])}:`;
const clock = (branch: Branch): Record<string, number> => ({ ...branch.bases, [branch.branchID]: branch.seq });
const mergeClocks = (...clocks: Readonly<Record<string, number>>[]): Record<string, number> => {
  const result: Record<string, number> = {};
  for (const entries of clocks) for (const [key, value] of Object.entries(entries)) {
    Object.defineProperty(result, key, { value: Math.max(Object.hasOwn(result, key) ? result[key] : 0, value), enumerable: true, configurable: true });
  }
  return result;
};
const validScope = (scope: DraftScope) => object(scope) && typeof scope.key === "string" && (
  id(scope.workspaceID) && (/^thread:[\w.-]{1,256}$/u.test(scope.key) || scope.key === `new:${scope.workspaceID}`) ||
  id(scope.workspaceID) && validFileEditScope(scope.key) ||
  (scope.workspaceID === "" || id(scope.workspaceID)) && /^queue-edit:[\w.-]{1,256}:[\w.-]{1,256}:[\w.-]{1,256}:[\w.-]{1,256}:\d{1,16}$/u.test(scope.key));

function validFileEditScope(key: string): boolean {
  if (!key.startsWith("file-edit:")) return false;
  try {
    const parts: unknown = JSON.parse(key.slice("file-edit:".length));
    return Array.isArray(parts) && parts.length === 2 && id(parts[0]) &&
      typeof parts[1] === "string" && parts[1].length > 0 && parts[1].length <= 4096 &&
      key === `file-edit:${JSON.stringify(parts)}`;
  } catch { return false; }
}

export function validDraftSnapshot(value: unknown, workspaceID: string): value is DraftSnapshot {
  return object(value) && typeof value.text === "string" && Array.isArray(value.files) && value.files.length <= 4 &&
    value.files.every((file: unknown) => object(file) && id(file.id) && typeof file.path === "string" &&
      file.path.length > 0 && file.path.length <= 4096 && typeof file.digest === "string" && /^[a-f0-9]{64}$/u.test(file.digest) &&
      typeof file.partial === "boolean" && typeof file.redacted === "boolean") &&
    new Set(value.files.map((file: V2FileReference) => file.id)).size === value.files.length &&
    validImageAttachments(value.images, workspaceID) &&
    (value.attachments === undefined || validFileAttachments(value.attachments, workspaceID)) &&
    (value.mcpReferences === undefined || validMCPReferences(value.mcpReferences));
}

function validBranch(value: unknown, scope: DraftScope): value is Branch {
  return object(value) && value.version === VERSION && object(value.scope) && value.scope.key === scope.key &&
    value.scope.workspaceID === scope.workspaceID && id(value.branchID) && id(value.ownerID) && seq(value.seq) &&
    object(value.bases) && !Object.hasOwn(value.bases, value.branchID) &&
    Object.entries(value.bases).every(([key, value]) => id(key) && seq(value)) && validDraftSnapshot(value.snapshot, scope.workspaceID);
}

/** Single-writer branch records. No read/modify/write of another window's key,
 * no shared mutable head index, no wall-clock last-writer-wins or storage CAS. */
export class DraftDocument {
  readonly ownerID: string;
  private readonly sessions = new Map<string, Session>();
  private readonly listeners = new Map<string, Set<() => void>>();

  constructor(private readonly store: V2RecoveryStore | null, options: { ownerID?: string } = {}) {
    this.ownerID = options.ownerID ?? crypto.randomUUID();
    if (!id(this.ownerID)) throw new Error(INVALID);
  }

  private session(scope: DraftScope, legacy?: DraftSnapshot): Session {
    if (!validScope(scope)) throw new Error(INVALID);
    const key = prefix(scope);
    let current = this.sessions.get(key);
    if (!current) {
      if (legacy !== undefined && !validDraftSnapshot(legacy, scope.workspaceID)) throw new Error(INVALID);
      current = { scope: clone(scope), legacy: clone(legacy ?? empty()), records: new Map(), versions: new Map(),
        unsaved: new Map(), selected: null, epoch: null, error: null };
      this.sessions.set(key, current);
    }
    return current;
  }

  private load(session: Session): void {
    if (!this.store) return;
    const keyPrefix = prefix(session.scope);
    try {
      const entries = this.store.entries<unknown>(keyPrefix);
      this.store.assertReadable(keyPrefix);
      for (const [key, value] of entries) {
        if (!validBranch(value, session.scope) || key !== keyPrefix + value.branchID) {
          this.store.reject(key);
          throw new Error(INVALID);
        }
        const current = session.records.get(value.branchID);
        if (current && current.seq === value.seq && JSON.stringify(current) !== JSON.stringify(value)) throw new Error(INVALID);
        if (!current || value.seq > current.seq) session.records.set(value.branchID, clone(value));
        this.remember(session, value);
      }
      session.error = null;
    } catch (failure) { session.error = failure instanceof Error ? failure.message : INVALID; }
  }

  private remember(session: Session, branch: Branch): void {
    session.versions.set(refKey(branchRef(branch)), clone(branch));
    // Persistent branch heads are never pruned. Only superseded, in-memory UI
    // versions expire; otherwise typing a long draft retains every text prefix.
    // Captures own their snapshot/vector and do not depend on this cache.
    const retained = new Set([...session.records.values()].map((record) => refKey(branchRef(record))));
    if (session.selected) retained.add(refKey(session.selected));
    const superseded = [...session.versions.keys()].filter((key) => !retained.has(key));
    for (const key of superseded.slice(0, Math.max(0, superseded.length - RECENT_UI_VERSIONS))) session.versions.delete(key);
  }

  private headBranches(session: Session): Branch[] {
    const records = [...session.records.values()];
    return records.filter((candidate) => !records.some((other) => other.branchID !== candidate.branchID &&
      Object.hasOwn(other.bases, candidate.branchID) && other.bases[candidate.branchID] >= candidate.seq))
      .sort((left, right) => left.branchID.localeCompare(right.branchID));
  }

  private state(session: Session, synchronize = true): DraftState {
    const heads = this.headBranches(session);
    const previous = session.selected;
    if (synchronize) {
      if (heads.length === 1) session.selected = branchRef(heads[0]);
      else if (heads.length > 1) {
        const selected = heads.find((head) => head.branchID === session.selected?.branchID);
        if (selected) session.selected = branchRef(selected);
        else if (!session.selected) session.selected = branchRef(heads[0]);
      }
      if (!sameRef(previous, session.selected) && session.epoch?.branchID !== session.selected?.branchID) session.epoch = null;
    }
    const selected = session.selected ? session.versions.get(refKey(session.selected)) : undefined;
    return {
      snapshot: clone(selected?.snapshot ?? session.legacy), ref: session.selected && { ...session.selected },
      heads: heads.map((branch) => ({ ref: branchRef(branch), snapshot: clone(branch.snapshot), ownerID: branch.ownerID })),
      conflict: heads.length > 1,
      headToken: JSON.stringify(heads.map((branch) => [branch.branchID, branch.seq])),
      error: session.error ?? session.unsaved.values().next().value ?? (this.store ? null : MEMORY_ONLY),
      persisted: !!this.store && !session.error && session.unsaved.size === 0,
    };
  }

  read(scope: DraftScope, legacySnapshot?: DraftSnapshot): DraftState {
    const session = this.session(scope, legacySnapshot);
    this.load(session);
    return this.state(session);
  }

  private captureSession(session: Session, basedOn: DraftRef | null = session.selected): DraftCapture {
    const branch = basedOn ? session.versions.get(refKey(basedOn)) : undefined;
    if (basedOn && !branch) throw new Error(CHANGED);
    if (!session.epoch || !sameRef(session.epoch.anchor, basedOn) && session.epoch.branchID !== basedOn?.branchID) {
      session.epoch = { branchID: crypto.randomUUID(), anchor: basedOn && { ...basedOn } };
    }
    return { scope: clone(session.scope), ref: basedOn && { ...basedOn }, branchID: session.epoch.branchID,
      ownerID: this.ownerID, snapshot: clone(branch?.snapshot ?? session.legacy), bases: branch ? clock(branch) : {} };
  }

  capture(scope: DraftScope, basedOn?: DraftRef | null): DraftCapture {
    // Preserve the version actually shown to the caller. A notification/read
    // explicitly adopts remote changes; starting an async action does not.
    const session = this.session(scope);
    return this.captureSession(session, basedOn === undefined ? session.selected : basedOn);
  }

  private save(session: Session, branch: Branch): void {
    if (branch.ownerID !== this.ownerID || !validBranch(branch, session.scope)) throw new Error(INVALID);
    session.records.set(branch.branchID, clone(branch));
    this.remember(session, branch);
    session.unsaved.set(branch.branchID, WRITE_FAILED);
    try {
      if (!this.store) throw new Error(MEMORY_ONLY);
      this.store.assertReadable(prefix(session.scope));
      this.store.write(prefix(session.scope) + branch.branchID, branch);
      session.unsaved.delete(branch.branchID);
    } catch (failure) { session.unsaved.set(branch.branchID, failure instanceof Error ? failure.message : WRITE_FAILED); }
    this.listeners.get(prefix(session.scope))?.forEach((listener) => listener());
  }

  private applyCapture(capture: DraftCapture, change: DraftUpdate, select: boolean): DraftState {
    if (capture.ownerID !== this.ownerID || !id(capture.branchID)) throw new Error(INVALID);
    const session = this.session(capture.scope);
    this.load(session);
    const current = session.records.get(capture.branchID);
    if (current && current.ownerID !== this.ownerID) throw new Error(INVALID);
    const previous = clone(current?.snapshot ?? capture.snapshot);
    const snapshot = typeof change === "function" ? change(previous) : { ...previous, ...change };
    if (!validDraftSnapshot(snapshot, session.scope.workspaceID)) throw new Error(INVALID);
    const bases = mergeClocks(current?.bases ?? {}, capture.bases);
    delete bases[capture.branchID];
    const branch: Branch = { version: VERSION, scope: clone(session.scope), branchID: capture.branchID, ownerID: this.ownerID,
      seq: (current?.seq ?? 0) + 1, bases, snapshot: clone(snapshot) };
    if (!seq(branch.seq)) throw new Error(INVALID);
    if (select) session.selected = branchRef(branch);
    this.save(session, branch);
    return this.state(session, select);
  }

  update(scope: DraftScope, change: DraftUpdate, basedOn?: DraftRef | null): DraftState {
    const session = this.session(scope);
    return this.applyCapture(this.captureSession(session, basedOn === undefined ? session.selected : basedOn), change, true);
  }

  updateCaptured(capture: DraftCapture, change: DraftUpdate): DraftState {
    const session = this.session(capture.scope);
    return this.applyCapture(capture, change, session.epoch?.branchID === capture.branchID);
  }

  resolve(scope: DraftScope, expectedHeadToken: string, selectedRef: DraftRef): DraftState {
    const current = this.read(scope);
    if (current.error) throw new Error(current.error);
    if (current.headToken !== expectedHeadToken) throw new Error(CHANGED);
    const session = this.session(scope);
    const selected = this.headBranches(session).find((head) => sameRef(branchRef(head), selectedRef));
    if (!selected) throw new Error(CHANGED);
    const bases = mergeClocks(...this.headBranches(session).map(clock));
    const branchID = crypto.randomUUID();
    session.epoch = { branchID, anchor: null };
    return this.applyCapture({ scope, ref: null, branchID, ownerID: this.ownerID, snapshot: selected.snapshot, bases }, {}, true);
  }

  settle(scope: DraftScope, ref: DraftRef): DraftState {
    const state = this.read(scope);
    if (state.error) return state;
    const session = this.session(scope);
    const original = this.headBranches(session).find((head) => sameRef(branchRef(head), ref));
    if (!original) return state;
    // Never overwrite the sending window's key: it can advance after this
    // read. A clearing descendant only consumes the exact observed sequence.
    const capture: DraftCapture = { scope, ref, branchID: crypto.randomUUID(), ownerID: this.ownerID,
      snapshot: original.snapshot, bases: clock(original) };
    const selected = sameRef(session.selected, ref);
    if (selected) session.epoch = { branchID: capture.branchID, anchor: ref };
    // Settlement replaces the whole sent snapshot, including optional fields.
    return this.applyCapture(capture, () => empty(), selected);
  }

  subscribe(scope: DraftScope, listener: () => void, notifications = this.store): () => void {
    this.session(scope);
    const key = prefix(scope);
    const listeners = this.listeners.get(key) ?? new Set();
    listeners.add(listener);
    this.listeners.set(key, listeners);
    const disconnect = notifications?.subscribePrefix?.(key, listener);
    return () => {
      disconnect?.();
      listeners.delete(listener);
      if (!listeners.size) this.listeners.delete(key);
    };
  }
}
