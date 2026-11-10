import { Component, type ReactNode } from "react";

type Props = { children: ReactNode };
type State = { error: Error | null };

export class AppErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  render() {
    if (this.state.error) {
      return <div style={{ padding: 24, fontFamily: "system-ui, sans-serif" }} role="alert">
        <h2 style={{ margin: "0 0 8px" }}>Something went wrong</h2>
        <p style={{ margin: "0 0 16px", color: "#666" }}>
          {this.state.error.message || "An unexpected error occurred."}
        </p>
        <button type="button" onClick={() => window.location.reload()}
          style={{ padding: "6px 16px", cursor: "pointer" }}>
          Reload
        </button>
      </div>;
    }
    return this.props.children;
  }
}
