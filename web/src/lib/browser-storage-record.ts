const recordStore = "records";
const recordKey = "current";
const connections = new Map<string, Promise<IDBDatabase>>();

function openRecordDatabase(name: string): Promise<IDBDatabase> {
  const existing = connections.get(name);
  if (existing) return existing;
  const opened = new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(name, 1);
    let blocked = false;
    request.onupgradeneeded = () => { request.result.createObjectStore(recordStore); };
    request.onblocked = () => {
      blocked = true;
      reject(new Error("Browser storage upgrade is blocked"));
    };
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      if (blocked) {
        db.close();
        return;
      }
      const forget = () => { if (connections.get(name) === opened) connections.delete(name); };
      db.onclose = forget;
      db.onversionchange = () => { db.close(); forget(); };
      resolve(db);
    };
  });
  connections.set(name, opened);
  void opened.catch(() => { if (connections.get(name) === opened) connections.delete(name); });
  return opened;
}

export type BrowserStorageRecordResult =
  | { ok: true; value: string | null; written: boolean }
  | { ok: false };

/** Read and optionally replace a shared record in one IndexedDB transaction.
 * The update runs synchronously while the transaction owns the object store.
 * Return undefined to leave the stored record untouched.
 */
export function updateBrowserStorageRecord(
  name: string,
  update: (current: string | null) => string | undefined,
  finish: (result: BrowserStorageRecordResult) => void,
  key = recordKey,
): void {
  void openRecordDatabase(name).then((db) => {
    let transaction: IDBTransaction | undefined;
    let value: string | null = null;
    let written = false;
    try {
      transaction = db.transaction(recordStore, "readwrite");
      transaction.oncomplete = () => finish({ ok: true, value, written });
      transaction.onabort = () => finish({ ok: false });
      const store = transaction.objectStore(recordStore);
      const request = store.get(key);
      request.onsuccess = () => {
        try {
          if (request.result !== undefined && typeof request.result !== "string") {
            throw new Error("Invalid browser storage record");
          }
          value = request.result ?? null;
          const next = update(value);
          if (next === undefined) return;
          store.put(next, key);
          value = next;
          written = true;
        } catch {
          transaction!.abort();
        }
      };
    } catch {
      if (transaction) transaction.abort();
      else finish({ ok: false });
    }
  }, () => finish({ ok: false }));
}
