import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { APIClient } from "../api/client";
import { LocaleProvider } from "../lib/locale";
import { NativeSkillImport } from "./native-skill-import";

function selectedFile(path: string, content: string) {
  const file = new File([content], path.split("/").at(-1)!);
  Object.defineProperties(file, {
    webkitRelativePath: { value: path },
    arrayBuffer: { value: async () => new TextEncoder().encode(content).buffer },
  });
  return file;
}

it("uploads selected bytes only after confirmation and preserves the operation on response loss", async () => {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const installSkillPackage = vi.fn().mockRejectedValueOnce(new Error("response lost"))
    .mockResolvedValueOnce({ installation: { state: "staged", manifest: { name: "review" } } });
  const user = userEvent.setup();
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider><NativeSkillImport
    client={{ hasSkillInstallation: true, installSkillPackage,
      extensionInventory: vi.fn().mockResolvedValue({ onboarding: { native_skill_directory_import: true } }) } as unknown as APIClient} /></LocaleProvider></QueryClientProvider>);
  await waitFor(() => expect(screen.getByRole("button", { name: "选择原生 Skill 目录" })).toBeEnabled());
  fireEvent.change(screen.getByLabelText("选择原生 Skill 目录"), { target: { files: [
    selectedFile("review/SKILL.md", "# Native skill\n"), selectedFile("review/references/example.txt", "example"),
  ] } });
  const submit = screen.getByRole("button", { name: "暂存原生 Skill" });
  expect(submit).toBeDisabled();
  expect(installSkillPackage).not.toHaveBeenCalled();
  await user.click(screen.getByRole("checkbox"));
  await user.click(submit);
  expect(await screen.findByRole("alert")).toHaveTextContent("response lost");
  await user.click(submit);
  expect(await screen.findByRole("status")).toHaveTextContent("staged");
  expect(installSkillPackage).toHaveBeenCalledTimes(2);
  expect(installSkillPackage.mock.calls[0]).toEqual(installSkillPackage.mock.calls[1]);
  expect(installSkillPackage.mock.calls[0][0]).toEqual({ version: "plugin-installation.v2", surface: "code", confirm_untrusted: true,
    directory: { root_name: "review", files: [
      { path: "SKILL.md", content_base64: btoa("# Native skill\n") },
      { path: "references/example.txt", content_base64: btoa("example") },
    ] },
  });
});

it("keeps directory uploads closed when an older service only supports ZIP installation", async () => {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  render(<QueryClientProvider client={new QueryClient()}><LocaleProvider><NativeSkillImport
    client={{ hasSkillInstallation: true, extensionInventory: vi.fn().mockResolvedValue({ onboarding: { plugin_import: true } }) } as unknown as APIClient}
  /></LocaleProvider></QueryClientProvider>);
  await screen.findByText(/当前服务或连接未开放原生 Skill 目录导入/);
  expect(screen.getByRole("button", { name: "选择原生 Skill 目录" })).toBeDisabled();
});
