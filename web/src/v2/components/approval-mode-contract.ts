// C owns this presentation seam and the eventual API/runtime wiring. These
// choices are approval preferences, never evidence of OS/provider authority.
export type ExecutionApprovalMode = "ask" | "auto" | "full";

export type FullActivationState = "inactive" | "active" | "unavailable";

export type ApprovalModeSelectionRequest =
  | { mode: "ask" | "auto"; confirmFull: false }
  | { mode: "full"; confirmFull: true };

export interface ApprovalModeControlProps {
  // The host projects legacy history before passing it to this component.
  mode: ExecutionApprovalMode;
  fullActivation: FullActivationState;
  fullUnavailableReason?: string;
  pending: boolean;
  disabled?: boolean;
  error?: string;
  variant?: "menu" | "settings";
  // A pre-creation choice has no running Thread whose activation can be shown.
  selectionOnly?: boolean;
  // A user action only. Never call on mount, history load, or prop changes.
  // Full selection/reactivation must follow explicit confirmation in the UI.
  onRequestChange: (request: ApprovalModeSelectionRequest) => void;
}
