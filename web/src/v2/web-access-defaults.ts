import { useCallback, useMemo } from "react";
import { canonicalWebAccessSettings } from "../api/client";
import type { WebAccessMode } from "../api/types";
import { useV2PersistentState } from "./recovery-storage";

export type WebAccessDefaults = { mode: WebAccessMode; targets: string[] };
const initial: WebAccessDefaults = { mode: "rules", targets: [] };

export function readWebAccessDefaults(value: unknown): WebAccessDefaults | null {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).some((key) => key !== "mode" && key !== "targets") ||
    !("mode" in value) || !("targets" in value)) return null;
  try { return canonicalWebAccessSettings(value.mode, value.targets); }
  catch { return null; }
}

// This is a new-conversation preference. The create request is still validated
// and persisted by Go; editing the preference cannot modify an existing task.
export function useV2WebAccessDefaults() {
  const [saved, setSaved] = useV2PersistentState<unknown>("web-access-defaults", initial);
  const settings = useMemo(() => readWebAccessDefaults(saved), [saved]);
  const onChange = useCallback((mode: WebAccessMode, targets: string[]) => {
    setSaved(canonicalWebAccessSettings(mode, targets));
  }, [setSaved]);
  return { ...(settings ?? initial), valid: settings !== null, onChange };
}
