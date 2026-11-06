import { ConnectionGate } from "./components/connection-gate";
import { useConnectionStore } from "./state/connection";
import { V2WorkbenchEntry } from "./v2";

// Task and resource pages share one shell and one connection lifetime.
export default function App() {
  const token = useConnectionStore((state) => state.token);
  return token ? <V2WorkbenchEntry /> : <ConnectionGate />;
}