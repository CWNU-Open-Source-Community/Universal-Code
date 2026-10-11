import { useLayoutEffect, useRef, type RefObject } from "react";

interface IsolatedElementState {
  count: number;
  ariaHidden: string | null;
  inert: string | null;
}

const isolatedElements = new Map<HTMLElement, IsolatedElementState>();

function isolateElement(element: HTMLElement): void {
  const current = isolatedElements.get(element);
  if (current) {
    current.count += 1;
    return;
  }
  isolatedElements.set(element, {
    count: 1,
    ariaHidden: element.getAttribute("aria-hidden"),
    inert: element.getAttribute("inert"),
  });
  element.setAttribute("aria-hidden", "true");
  element.setAttribute("inert", "");
}

function restoreElement(element: HTMLElement): void {
  const current = isolatedElements.get(element);
  if (!current) return;
  current.count -= 1;
  if (current.count > 0) return;
  if (current.ariaHidden === null) element.removeAttribute("aria-hidden");
  else element.setAttribute("aria-hidden", current.ariaHidden);
  if (current.inert === null) element.removeAttribute("inert");
  else element.setAttribute("inert", current.inert);
  isolatedElements.delete(element);
}

function modalBackgroundElements(dialog: HTMLElement): HTMLElement[] {
  const background = new Set<HTMLElement>();
  let branch: HTMLElement = dialog;
  while (branch.parentElement) {
    const parent = branch.parentElement;
    for (const sibling of parent.children) {
      if (sibling !== branch && sibling instanceof HTMLElement) background.add(sibling);
    }
    if (parent === document.body) break;
    branch = parent;
  }
  return [...background];
}

const focusableSelector = [
  "a[href]",
  "area[href]",
  "button:not([disabled])",
  "input:not([disabled]):not([type='hidden'])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[contenteditable='true']",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

function focusableElements(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(focusableSelector)).filter((element) =>
    !element.matches(":disabled") &&
    !element.closest("[hidden], [inert], [aria-hidden='true']"));
}

export function useModalFocusTrap<T extends HTMLElement>(open: boolean, onEscape: () => void,
  escapeDisabled = false, initialFocusRef?: RefObject<HTMLElement | null>, options?: {
    isolateBackground?: boolean;
    trapFocus?: boolean;
    returnFocusRef?: RefObject<HTMLElement | null>;
  }) {
  const dialogRef = useRef<T>(null);
  const onEscapeRef = useRef(onEscape);
  const escapeDisabledRef = useRef(escapeDisabled);
  onEscapeRef.current = onEscape;
  escapeDisabledRef.current = escapeDisabled;

  useLayoutEffect(() => {
    if (!open || !dialogRef.current) return;
    const dialog = dialogRef.current;
    const previouslyFocused = document.activeElement instanceof HTMLElement
      ? document.activeElement : null;
    const isolated = options?.isolateBackground ? modalBackgroundElements(dialog) : [];
    isolated.forEach(isolateElement);
    const candidates = focusableElements(dialog);
    const preferred = initialFocusRef?.current && dialog.contains(initialFocusRef.current)
      ? initialFocusRef.current
      : candidates.find((element) => element.hasAttribute("autofocus")) ?? candidates[0];
    if (!dialog.contains(document.activeElement)) {
      (preferred ?? dialog).focus();
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !escapeDisabledRef.current) {
        event.preventDefault();
        event.stopPropagation();
        onEscapeRef.current();
        return;
      }
      if (options?.trapFocus === false || event.key !== "Tab" || event.altKey || event.ctrlKey || event.metaKey) return;
      const currentCandidates = focusableElements(dialog);
      if (currentCandidates.length === 0) {
        event.preventDefault();
        dialog.focus();
        return;
      }
      const first = currentCandidates[0];
      const last = currentCandidates.at(-1)!;
      const active = document.activeElement;
      if (event.shiftKey && (active === first || !dialog.contains(active))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (active === last || !dialog.contains(active))) {
        event.preventDefault();
        first.focus();
      }
    };
    dialog.addEventListener("keydown", handleKeyDown);
    return () => {
      dialog.removeEventListener("keydown", handleKeyDown);
      isolated.forEach(restoreElement);
      const returnFocus = options?.returnFocusRef?.current;
      if (returnFocus?.isConnected) returnFocus.focus();
      else if (previouslyFocused?.isConnected) previouslyFocused.focus();
    };
  }, [open, options?.isolateBackground, options?.trapFocus]);

  return dialogRef;
}
