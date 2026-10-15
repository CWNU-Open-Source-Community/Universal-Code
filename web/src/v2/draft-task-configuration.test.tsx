import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { useV2DraftTaskConfiguration } from "./draft-task-configuration";
import { V2RecoveryProvider } from "./recovery-storage";

afterEach(() => window.localStorage.clear());
const wrapper = ({ children }: { children: ReactNode }) => <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="budget-drafts">{children}</V2RecoveryProvider>;

it("retains exact Skill choices across budget edits and reopening without clearing a known rejection", () => {
  const first = renderHook(() => useV2DraftTaskConfiguration("workspace-first"), { wrapper });
  const choice = { refs: ["code@1.0.0"], confirmUntrustedContext: true };
  act(() => { first.result.current.onValidityChange(false); first.result.current.onProjectSkillsChange(choice); });
  act(() => first.result.current.onBudgetChange({ max_turns: 20 }));
  expect(first.result.current.valid).toBe(false);
  expect(first.result.current.projectSkills).toEqual(choice);
  first.unmount();
  const reopened = renderHook(() => useV2DraftTaskConfiguration("workspace-first"), { wrapper });
  expect(reopened.result.current.projectSkills).toEqual(choice);
  expect(reopened.result.current.budget).toEqual({ max_turns: 20 });
  expect(reopened.result.current.valid).toBe(false);
});

it("persists a native component pin unchanged across reopening", () => {
  const ref = JSON.stringify({ installation_id: "native-install", package_id: "package-" + "p".repeat(120), component_id: "skill:native",
    revision: "a".repeat(64), installation_generation: 7 });
  const choice = { refs: [ref], confirmUntrustedContext: true, source: "installed" as const };
  const first = renderHook(() => useV2DraftTaskConfiguration("workspace-native"), { wrapper });
  act(() => first.result.current.onProjectSkillsChange(choice));
  first.unmount();
  const reopened = renderHook(() => useV2DraftTaskConfiguration("workspace-native"), { wrapper });
  expect(reopened.result.current.projectSkills).toEqual(choice);
});

it("retains each workspace's budget and rejection without letting an old callback update another workspace", () => {
  const view = renderHook(({ workspaceID }) => useV2DraftTaskConfiguration(workspaceID), { initialProps: { workspaceID: "workspace-first" }, wrapper });
  act(() => view.result.current.onBudgetChange({ max_turns: 20 }));
  const firstValidity = view.result.current.onValidityChange;
  view.rerender({ workspaceID: "workspace-second" });
  expect(view.result.current.budget).toBeUndefined();
  expect(view.result.current.valid).toBe(true);
  act(() => { view.result.current.onBudgetChange({ max_tool_calls: 30 }); firstValidity(false); });
  expect(view.result.current.budget).toEqual({ max_tool_calls: 30 });
  expect(view.result.current.valid).toBe(true);
  view.rerender({ workspaceID: "workspace-first" });
  expect(view.result.current.budget).toEqual({ max_turns: 20 });
  expect(view.result.current.valid).toBe(false);
});

it("keeps incomplete numeric input invalid after persistence, unmount and reopening", () => {
  const first = renderHook(() => useV2DraftTaskConfiguration("workspace-first"), { wrapper });
  act(() => first.result.current.onBudgetChange({ max_tokens: Number.NaN }));
  expect(first.result.current.valid).toBe(false);
  first.unmount();
  const reopened = renderHook(() => useV2DraftTaskConfiguration("workspace-first"), { wrapper });
  expect(reopened.result.current.budget?.max_tokens).toBeNaN();
  expect(reopened.result.current.valid).toBe(false);
  act(() => reopened.result.current.onValidityChange(true));
  expect(reopened.result.current.valid).toBe(false);
  act(() => reopened.result.current.onBudgetChange(undefined));
  expect(reopened.result.current.budget).toBeUndefined();
  expect(reopened.result.current.valid).toBe(false);
  act(() => reopened.result.current.onValidityChange(true));
  expect(reopened.result.current.valid).toBe(true);
});

it("preserves a known project rejection through valid budget edits and restoring defaults until a completed preview clears it", () => {
  const view = renderHook(() => useV2DraftTaskConfiguration("workspace-first"), { wrapper });
  act(() => view.result.current.onValidityChange(false));
  act(() => view.result.current.onBudgetChange({ max_turns: 20 }));
  expect(view.result.current.budget).toEqual({ max_turns: 20 });
  expect(view.result.current.valid).toBe(false);
  act(() => view.result.current.onBudgetChange(undefined));
  expect(view.result.current.valid).toBe(false);
  act(() => view.result.current.onValidityChange(true));
  expect(view.result.current.valid).toBe(true);
});

it("also retains per-workspace draft settings when durable recovery is unavailable", () => {
  const view = renderHook(({ workspaceID }) => useV2DraftTaskConfiguration(workspaceID), { initialProps: { workspaceID: "workspace-first" } });
  act(() => view.result.current.onBudgetChange({ timeout_seconds: 60 }));
  view.rerender({ workspaceID: "workspace-second" });
  expect(view.result.current.budget).toBeUndefined();
  view.rerender({ workspaceID: "workspace-first" });
  expect(view.result.current.budget).toEqual({ timeout_seconds: 60 });
});

it("isolates budget drafts between clients even when the workspace identifier is the same", () => {
  let baseURL = "/api/client-first";
  const scopedWrapper = ({ children }: { children: ReactNode }) => <V2RecoveryProvider client={{ baseURL }} scopeID="budget-drafts">{children}</V2RecoveryProvider>;
  const view = renderHook(() => useV2DraftTaskConfiguration("workspace-shared-id"), { wrapper: scopedWrapper });
  act(() => view.result.current.onBudgetChange({ max_turns: 20 }));
  const originalBudgetChange = view.result.current.onBudgetChange;
  baseURL = "/api/client-second";
  view.rerender();
  expect(view.result.current.budget).toBeUndefined();
  act(() => originalBudgetChange({ max_turns: 15 }));
  expect(view.result.current.budget).toBeUndefined();
  expect(view.result.current.valid).toBe(true);
  baseURL = "/api/client-first";
  view.rerender();
  expect(view.result.current.budget).toEqual({ max_turns: 15 });
});
