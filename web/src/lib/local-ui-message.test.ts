import { localUIMessage } from "./local-ui-message";

it("translates known local recovery messages only at display time", () => {
  const original = "这版草稿尚未保存到本机；内容仍在本窗口，刷新或退出可能丢失。";
  expect(localUIMessage(original, (_zh, en) => en)).toContain("may be lost on reload or exit");
  expect(localUIMessage(original, (zh) => zh)).toBe(original);
  expect(localUIMessage("服务返回的原始错误：文件未找到", (_zh, en) => en)).toBe("服务返回的原始错误：文件未找到");
});
