import { AppErrorBoundary } from "./components/app-error-boundary";
import { ConnectionGate } from "./components/connection-gate";
import { useConnectionStore } from "./state/connection";
import { V2WorkbenchEntry } from "./v2";

// Task and resource pages share one shell and one connection lifetime.
// The error boundary prevents a render-time crash from producing a white
// screen — the user sees an error message with a reload button instead.
export default function App() {
  const token = useConnectionStore((state) => state.token);
  return <AppErrorBoundary>
    {token ? <V2WorkbenchEntry /> : <ConnectionGate />}
  </AppErrorBoundary>;
}