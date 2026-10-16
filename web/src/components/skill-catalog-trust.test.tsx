import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import { LocaleProvider } from "../lib/locale";
import { SkillCatalogTrust } from "./skill-catalog-trust";

it("uses a fresh explicit confirmation and version when restoring revoked Catalog trust", async () => {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  let publisher = { fingerprint: "a".repeat(64), public_key: btoa("a".repeat(32)), name: "Team", team: "",
    trust_class: "trusted", created_at: "2026-10-16T00:00:00Z", updated_at: "2026-10-16T00:00:01Z" };
  const listSkillCatalogPublishers = vi.fn(async () => ({ protocol_version: "skill-catalog-control.v1", control_enabled: true, publishers: [publisher] }));
  const controlSkillCatalogPublisher = vi.fn(async (body: { action: string }) => {
    publisher = { ...publisher, trust_class: body.action === "trust" ? "trusted" : "revoked", updated_at: body.action === "trust" ? "2026-10-16T00:00:03Z" : "2026-10-16T00:00:02Z" };
    return publisher;
  });
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider><SkillCatalogTrust client={{
    hasSkillInstallation: true, listSkillCatalogPublishers, controlSkillCatalogPublisher,
  } as unknown as APIClient} /></LocaleProvider></QueryClientProvider>);
  const user = userEvent.setup();
  expect(listSkillCatalogPublishers).not.toHaveBeenCalled();
  await user.click(screen.getByText("旧 Skill Catalog 发布者信任"));
  const revoke = await screen.findByRole("button", { name: "撤销 Catalog 信任" });
  expect(revoke).toBeDisabled();
  await user.click(screen.getByRole("checkbox", { name: "确认撤销此 Catalog 发布者信任" }));
  await user.click(revoke);
  const regrant = await screen.findByRole("button", { name: "重新信任 Catalog 发布者" });
  expect(regrant).toBeDisabled();
  await user.click(screen.getByRole("checkbox", { name: "已重新核对来源，确认恢复此 Catalog 发布者信任" }));
  await user.click(regrant);
  expect(await screen.findByRole("button", { name: "撤销 Catalog 信任" })).toBeDisabled();
  expect(controlSkillCatalogPublisher.mock.calls[0][0]).toMatchObject({ action: "revoke", expected_updated_at: "2026-10-16T00:00:01Z" });
  expect(controlSkillCatalogPublisher.mock.calls[1][0]).toMatchObject({ action: "trust", expected_updated_at: "2026-10-16T00:00:02Z", public_key: publisher.public_key });
});
