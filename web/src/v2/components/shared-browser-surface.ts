import { useEffect, useRef } from "react";
import { nextSharedBrowserLayoutRevision, setSharedBrowserSurface, type SharedBrowserState } from "../../lib/desktop-shared-browser";

const owners = new Map<string, symbol>();
export async function hideUnclaimedSharedBrowser(state: SharedBrowserState) {
  if (owners.has(state.session_id)) return;
  await setSharedBrowserSurface({ session_id: state.session_id, tab_id: state.active_tab_id,
    bounds: { x: 0, y: 0, width: 0, height: 0 }, device_scale_factor: window.devicePixelRatio || 1,
    visible: false, layout_revision: nextSharedBrowserLayoutRevision() });
}

export function sharedBrowserOccluded(surface: HTMLElement): boolean {
  if (document.visibilityState === "hidden" || surface.closest('[inert], [hidden], [data-resizing="true"]')) return true;
  for (let node: HTMLElement | null = surface; node; node = node.parentElement) {
    const style = getComputedStyle(node);
    if (style.display === "none" || style.visibility === "hidden") return true;
  }
  return Array.from(document.querySelectorAll<HTMLElement>('[aria-modal="true"], [role="dialog"], [role="menu"], dialog[open], [data-shared-browser-occluder]'))
    .some((overlay) => {
      // The narrow workspace itself is a dialog, and owns this surface.
      if (overlay.contains(surface) || overlay.closest('[hidden], [inert]')) return false;
      const style = getComputedStyle(overlay);
      return style.display !== "none" && style.visibility !== "hidden" && overlay.getClientRects().length > 0;
    });
}

// DOM supplies geometry only. The host attaches the actual browser view here.
export function useSharedBrowserSurface(state: SharedBrowserState | null, onError: (error: unknown) => void) {
  const ref = useRef<HTMLDivElement>(null);
  const errorRef = useRef(onError);
  errorRef.current = onError;
  useEffect(() => {
    const surface = ref.current;
    if (!surface || !state?.active_tab_id) return;
    const { session_id, active_tab_id: tab_id } = state;
    const owner = Symbol(session_id);
    owners.set(session_id, owner);
    let frame = 0;
    let signature = "";
    let disposed = false;
    let followUntil = 0;
    const send = (visible: boolean, bounds: { x: number; y: number; width: number; height: number }) => {
      const device_scale_factor = window.devicePixelRatio || 1;
      const next = JSON.stringify([visible, bounds, device_scale_factor]);
      if (signature === next) return;
      signature = next;
      if (owners.get(session_id) !== owner) return;
      void setSharedBrowserSurface({ session_id, tab_id, bounds, device_scale_factor, visible, layout_revision: nextSharedBrowserLayoutRevision() })
        .catch((error) => { if (!disposed) errorRef.current(error); });
    };
    const update = () => {
      frame = 0;
      if (disposed) return;
      const rect = surface.getBoundingClientRect();
      const bounds = { x: Math.max(0, rect.x), y: Math.max(0, rect.y),
        width: Math.max(0, Math.min(rect.right, window.innerWidth) - Math.max(0, rect.x)),
        height: Math.max(0, Math.min(rect.bottom, window.innerHeight) - Math.max(0, rect.y)) };
      send(state.state === "ready" && bounds.width > 0 && bounds.height > 0 && !sharedBrowserOccluded(surface), bounds);
      if (performance.now() < followUntil) frame = requestAnimationFrame(update);
    };
    const schedule = () => { if (!frame) frame = requestAnimationFrame(update); };
    const transition = (event: Event) => {
      if (event.target instanceof Element && event.target.contains(surface)) {
        followUntil = performance.now() + 650;
        schedule();
      }
    };
    const resize = new ResizeObserver(schedule);
    resize.observe(surface);
    const mutations = new MutationObserver(schedule);
    mutations.observe(document.body, { attributes: true, childList: true, subtree: true,
      attributeFilter: ["class", "style", "hidden", "inert", "role", "aria-modal", "open", "data-resizing"] });
    window.addEventListener("resize", schedule);
    window.addEventListener("scroll", schedule, true);
    document.addEventListener("visibilitychange", schedule);
    document.addEventListener("transitionrun", transition, true);
    schedule();
    return () => {
      disposed = true;
      cancelAnimationFrame(frame);
      resize.disconnect(); mutations.disconnect();
      window.removeEventListener("resize", schedule);
      window.removeEventListener("scroll", schedule, true);
      document.removeEventListener("visibilitychange", schedule);
      document.removeEventListener("transitionrun", transition, true);
      send(false, { x: 0, y: 0, width: 0, height: 0 });
      if (owners.get(session_id) === owner) owners.delete(session_id);
    };
  }, [state?.session_id, state?.active_tab_id, state?.state]);
  return ref;
}
