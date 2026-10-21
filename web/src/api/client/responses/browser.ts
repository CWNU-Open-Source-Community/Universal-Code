import { APIRequestError } from "../protocol";
import { boundedIdentity, boundedText, hasExactKeys, hasOnlyKeys, isRecord, validDate } from "../validation";
import type { FullCDPSessionControlView, FullCDPSessionView } from "../../types";

export function parseFullCDPSession(value: unknown, expectedRunID: string): FullCDPSessionView {
  const required = ["cdp_closed", "process_tree_quiescent", "profile_cleaned",
    "profile_released", "run_id", "runtime_available", "state", "version"];
  const optional = ["browser", "close_reason", "completed_at", "expires_at", "failure_code",
    "session_id", "started_at", "target_origin"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.version !== "full_cdp_session.v1" || value.run_id !== expectedRunID ||
    !boundedIdentity(value.run_id) ||
    !["none", "starting", "ready", "closing", "closed", "failed"]
      .includes(String(value.state)) ||
    typeof value.runtime_available !== "boolean" || typeof value.cdp_closed !== "boolean" ||
    typeof value.process_tree_quiescent !== "boolean" ||
    typeof value.profile_released !== "boolean" || typeof value.profile_cleaned !== "boolean" ||
    (value.session_id !== undefined && !boundedIdentity(value.session_id)) ||
    (value.target_origin !== undefined && !boundedText(value.target_origin, 2_048)) ||
    (value.started_at !== undefined && !validDate(value.started_at)) ||
    (value.expires_at !== undefined && !validDate(value.expires_at)) ||
    (value.completed_at !== undefined && !validDate(value.completed_at)) ||
    (value.failure_code !== undefined && !/^[a-z_]{1,96}$/u.test(String(value.failure_code))) ||
    (value.close_reason !== undefined && !["operator_closed", "expired", "permission_revoked",
      "process_exited", "run_terminal", "desktop_shutdown", "open_failed"]
      .includes(String(value.close_reason)))) {
    throw new APIRequestError("Full CDP session response is invalid", "INVALID_RESPONSE", 502);
  }
  const noSession = value.state === "none";
  if (noSession !== (value.session_id === undefined) ||
    noSession !== (value.browser === undefined) ||
    (value.profile_cleaned && !value.profile_released) ||
    ((value.state === "ready" || value.state === "closed") &&
      (!validDate(value.started_at) || !validDate(value.expires_at) ||
        !boundedText(value.target_origin, 2_048))) ||
    (value.state === "closing" &&
      [value.started_at, value.expires_at, value.target_origin].filter(
        (item) => item !== undefined).length !== 0 &&
      (!validDate(value.started_at) || !validDate(value.expires_at) ||
        !boundedText(value.target_origin, 2_048))) ||
    ((value.state === "closed" || value.state === "failed") !==
      (value.completed_at !== undefined)) ||
    (value.state === "closed" && (!value.cdp_closed || !value.process_tree_quiescent ||
      !value.profile_released || !value.profile_cleaned || value.failure_code !== undefined ||
      value.close_reason === undefined))) {
    throw new APIRequestError("Full CDP session lifecycle response is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  if (value.browser !== undefined && (!hasExactKeys(value.browser, ["channel", "product"]) ||
    !["chrome", "edge"].includes(String(value.browser.product)) ||
    !["stable", "beta", "dev", "canary"].includes(String(value.browser.channel)))) {
    throw new APIRequestError("Full CDP browser selection response is invalid",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as FullCDPSessionView;
}

export function parseFullCDPSessionControl(value: unknown, expectedRunID: string):
  FullCDPSessionControlView {
  if (!hasExactKeys(value, ["replayed", "session"]) || typeof value.replayed !== "boolean") {
    throw new APIRequestError("Full CDP session control response is invalid",
      "INVALID_RESPONSE", 502);
  }
  return { ...value, session: parseFullCDPSession(value.session, expectedRunID) } as
    unknown as FullCDPSessionControlView;
}
