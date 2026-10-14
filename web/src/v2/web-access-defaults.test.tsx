import type { ReactNode } from "react";
import { act, cleanup, renderHook } from "@testing-library/react";
import { V2RecoveryProvider } from "./recovery-storage";
import { readWebAccessDefaults, useV2WebAccessDefaults } from "./web-access-defaults";

function wrapper({ children }: { children: ReactNode }) {
  return <V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="web-access-db">{children}</V2RecoveryProvider>;
}
afterEach(() => { cleanup(); window.localStorage.clear(); });

it("remembers an explicit full choice after remount and replaces it with disabled", () => {
  const first = renderHook(useV2WebAccessDefaults, { wrapper });
  expect(first.result.current).toMatchObject({ mode: "rules", targets: [], valid: true });
  act(() => first.result.current.onChange("full", []));
  first.unmount();
  const reopened = renderHook(useV2WebAccessDefaults, { wrapper });
  expect(reopened.result.current).toMatchObject({ mode: "full", targets: [], valid: true });
  act(() => reopened.result.current.onChange("disabled", []));
  reopened.unmount();
  expect(renderHook(useV2WebAccessDefaults, { wrapper }).result.current.mode).toBe("disabled");
});

it("stores canonical rule hosts and retains them after reopening", () => {
  const first = renderHook(useV2WebAccessDefaults, { wrapper });
  act(() => first.result.current.onChange("rules", ["HTTPS://Docs.Example.com/", "docs.example.com", "api.example.com"]));
  first.unmount();
  expect(renderHook(useV2WebAccessDefaults, { wrapper }).result.current).toMatchObject({
    mode: "rules", targets: ["api.example.com", "docs.example.com"], valid: true,
  });
});

it.each([
  { mode: "cached", targets: [] }, { mode: "indexed", targets: [] },
  { mode: "full", targets: ["example.com"] }, { mode: "rules", targets: ["*"] },
  { mode: "full" }, { mode: "full", targets: [], extra: true }, null,
])("does not interpret invalid persisted settings as an access grant: %j", (value) => {
  expect(readWebAccessDefaults(value)).toBeNull();
});
