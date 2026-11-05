import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import App from "./App";
import { useConnectionStore } from "./state/connection";
import { useV2Navigation } from "./v2/navigation";

vi.mock("./components/connection-gate", () => ({ ConnectionGate: () => <main data-testid="connection-gate" /> }));
vi.mock("./v2", () => ({
  V2WorkbenchEntry: () => {
    const [localState, setLocalState] = useState("");
    const { route } = useV2Navigation();
    return <main data-testid="v2-workbench"><output data-testid="shell-route">{JSON.stringify(route)}</output>
      <input aria-label="Shell local state fixture" value={localState} onChange={(event) => setLocalState(event.target.value)} />
    </main>;
  },
}));

function connect() {
  useConnectionStore.getState().connect("read-token-fixture", {
    status: "ok", api_version: "api.v1", app_version: "test", schema_version: 129,
  }, "control-token-fixture", { threadControlEnabled: true });
}

afterEach(() => {
  cleanup();
  useConnectionStore.getState().disconnect();
  window.history.replaceState({}, "", "/");
});

it("uses the existing connection gate before mounting the shared shell", () => {
  useConnectionStore.getState().disconnect();
  render(<App />);
  expect(screen.getByTestId("connection-gate")).toBeInTheDocument();
  expect(screen.queryByTestId("v2-workbench")).not.toBeInTheDocument();
  act(connect);
  expect(screen.getByTestId("v2-workbench")).toBeInTheDocument();
  expect(screen.queryByTestId("connection-gate")).not.toBeInTheDocument();
});

it.each([
  ["/", { kind: "initial" }],
  ["/?legacy=1", { kind: "initial" }],
  ["/legacy", { kind: "invalid" }],
  ["/legacy/threads/thread-url", { kind: "invalid" }],
  ["/legacy/runs/run-url", { kind: "invalid" }],
  ["/legacy/sessions/session-url", { kind: "invalid" }],
] as const)("keeps %s inside the one connected shell", (url, route) => {
  connect();
  useConnectionStore.getState().selectThread("unrelated-previous-thread");
  window.history.replaceState({}, "", url);
  render(<App />);
  expect(screen.getByTestId("v2-workbench")).toBeInTheDocument();
  expect(JSON.parse(screen.getByTestId("shell-route").textContent!)).toEqual(route);
  expect(screen.queryByTestId("connection-gate")).not.toBeInTheDocument();
  expect(document.querySelector(".prayu-shell")).not.toBeInTheDocument();
});

it("does not replace the shell or connection while moving between task and resource pages", () => {
  connect();
  window.history.replaceState({}, "", "#/threads/thread-url");
  render(<App />);
  const shell = screen.getByTestId("v2-workbench");
  fireEvent.change(screen.getByLabelText("Shell local state fixture"), { target: { value: "shared shell state" } });
  for (const url of ["/#/threads/thread-url/records", "/#/threads/thread-url/runs/run-1/settings/appearance",
    "/#/runs/run-1", "/#/history", "/#/threads/thread-url"]) {
    act(() => { window.history.pushState({}, "", url); window.dispatchEvent(new PopStateEvent("popstate")); });
    expect(screen.getByTestId("v2-workbench")).toBe(shell);
    expect(screen.getByLabelText("Shell local state fixture")).toHaveValue("shared shell state");
    expect(useConnectionStore.getState().token).toBe("read-token-fixture");
    expect(useConnectionStore.getState().controlToken).toBe("control-token-fixture");
  }
});
