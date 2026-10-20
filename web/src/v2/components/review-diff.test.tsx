import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { render as renderComponent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parseReviewDiff, ReviewDiff } from "./review-diff";

const patch = "--- a/src/app.ts\n+++ b/src/app.ts\n@@ -7,3 +9,3 @@ result\n unchanged\n-old value\n+new value\n last\n\\ No newline at end of file";

it("keeps original and proposed line numbers separate when citing an older diff", async () => {
  const feedback = vi.fn();
  const source = "来源执行：old-run\n编辑记录：old-edit\n原版本：old-sha\n提案版本：proposed-sha\n当前观测版本：later-sha";
  render(<ReviewDiff patch={patch} source={source} onFeedback={feedback} />);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "引用旧第 8 行" }));
  expect(feedback).toHaveBeenLastCalledWith(`${source}\n旧文件路径：a/src/app.ts\n新文件路径：b/src/app.ts\n差异片段：@@ -7,3 +9,3 @@ result\n旧文件第 8 行：\n-old value\n行号只对应上述差异版本，请先核对当前内容。`);
  await user.click(screen.getByRole("button", { name: "引用新第 10 行" }));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining("新文件第 10 行：\n+new value"));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining("当前观测版本：later-sha"));
  expect(screen.queryByRole("button", { name: /No newline/ })).not.toBeInTheDocument();
});

it("cites the exact selected hunk and explicitly bounds long excerpts", async () => {
  const feedback = vi.fn();
  const longPatch = `${patch}\n@@ -100,0 +103,62 @@ added\n${Array.from({ length: 62 }, (_, i) => `+line ${i + 1}`).join("\n")}`;
  render(<ReviewDiff patch={longPatch} source="source binding" onFeedback={feedback} />);
  await userEvent.setup().click(within(screen.getByRole("region", { name: "差异片段 2" })).getByRole("button", { name: "引用此片段" }));
  const result = feedback.mock.calls[0][0] as string;
  expect(result).toContain("source binding\n旧文件路径：a/src/app.ts\n新文件路径：b/src/app.ts\n差异片段：@@ -100,0 +103,62 @@ added");
  expect(result).toContain("+line 60\n（摘录前60行）");
  expect(result).not.toContain("+line 61");
  expect(result).not.toContain("old value");
});

it("does not invent source line numbers for a patch without hunk headers", () => {
  const parsed = parseReviewDiff("binary or legacy diff\n-old\n+new");
  expect(parsed.hunks).toEqual([]);
  render(<ReviewDiff patch="binary or legacy diff\n-old\n+new" source="historical source" onFeedback={vi.fn()} />);
  expect(screen.getByText(/binary or legacy diff/)).toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("keeps each file boundary and cites only the selected file's hunk", async () => {
  const feedback = vi.fn();
  const multi = `${patch}\ndiff --git a/src/other.ts b/src/other.ts\nindex aaaa..bbbb 100644\n--- a/src/other.ts\n+++ b/src/other.ts\n@@ -21 +30,2 @@ other\n-old other\n+new other\n+extra other\n@@ -50 +60 @@ second\n-last old\n+last new`;
  const parsed = parseReviewDiff(multi);
  expect(parsed.hunks).toHaveLength(3);
  expect(parsed.hunks[0].lines.map((line) => line.text)).toEqual([" unchanged", "-old value", "+new value", " last", "\\ No newline at end of file"]);
  expect(parsed.hunks[0]).toMatchObject({ oldPath: "a/src/app.ts", newPath: "b/src/app.ts" });
  expect(parsed.hunks[1]).toMatchObject({ oldPath: "a/src/other.ts", newPath: "b/src/other.ts" });
  expect(parsed.hunks[2]).toMatchObject({ oldPath: "a/src/other.ts", newPath: "b/src/other.ts" });
  render(<ReviewDiff patch={multi} source="Git preview exact-fingerprint" onFeedback={feedback} />);
  const selected = within(screen.getByRole("region", { name: "差异片段 2" }));
  await userEvent.setup().click(selected.getByRole("button", { name: "引用此片段" }));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining("旧文件路径：a/src/other.ts\n新文件路径：b/src/other.ts"));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining("@@ -21 +30,2 @@ other\n-old other\n+new other\n+extra other"));
  expect(feedback.mock.calls[0][0]).not.toContain("src/app.ts");
  expect(feedback.mock.calls[0][0]).not.toContain("last new");
  await userEvent.setup().click(selected.getByRole("button", { name: "引用新第 31 行" }));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining("新文件路径：b/src/other.ts\n差异片段：@@ -21 +30,2 @@ other\n新文件第 31 行：\n+extra other"));
});

it.each([
  { name: "rename", oldPath: '"a/旧 name.ts"', newPath: '"b/new name.ts"', header: "@@ -3 +4 @@", lines: "-before\n+after", button: "引用旧第 3 行" },
  { name: "delete", oldPath: "a/deleted.txt", newPath: "/dev/null", header: "@@ -5 +0,0 @@", lines: "-removed", button: "引用旧第 5 行" },
  { name: "create", oldPath: "/dev/null", newPath: "b/new.txt", header: "@@ -0,0 +1 @@", lines: "+created", button: "引用新第 1 行" },
])("preserves raw old/new paths for $name including zero-count sides", async ({ oldPath, newPath, header, lines, button }) => {
  const feedback = vi.fn(), value = `--- ${oldPath}\n+++ ${newPath}\n${header}\n${lines}`;
  const parsed = parseReviewDiff(value);
  expect(parsed.hunks[0]).toMatchObject({ oldPath, newPath });
  render(<ReviewDiff patch={value} source="historical edit/source hash" onFeedback={feedback} />);
  await userEvent.setup().click(screen.getByRole("button", { name: button }));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining(`historical edit/source hash\n旧文件路径：${oldPath}\n新文件路径：${newPath}\n差异片段：${header}`));
});

it("uses declared remaining lines to distinguish source ---/+++ text from following file headers", () => {
  const value = "--- a/markers.txt\n+++ b/markers.txt\n@@ -4,2 +8,2 @@\n--- old content\n+++ new content\n --- shared prefix\n--- a/next.txt\n+++ b/next.txt\n@@ -1 +1 @@\n-next old\n+next new";
  const parsed = parseReviewDiff(value);
  expect(parsed.hunks).toHaveLength(2);
  expect(parsed.hunks[0]).toMatchObject({ oldPath: "a/markers.txt", newPath: "b/markers.txt", lines: [
    { text: "--- old content", kind: "remove", oldLine: 4 },
    { text: "+++ new content", kind: "add", newLine: 8 },
    { text: " --- shared prefix", kind: "context", oldLine: 5, newLine: 9 },
  ] });
  expect(parsed.hunks[1]).toMatchObject({ oldPath: "a/next.txt", newPath: "b/next.txt", lines: [
    { text: "-next old", kind: "remove", oldLine: 1 }, { text: "+next new", kind: "add", newLine: 1 },
  ] });
});

it("does not guess missing file paths from other metadata and keeps the original source", async () => {
  const feedback = vi.fn();
  render(<ReviewDiff patch={"diff --git a/possible b/possible\n@@ -1 +1 @@\n-old\n+new"} source="retained operation binding" onFeedback={feedback} />);
  await userEvent.setup().click(screen.getByRole("button", { name: "引用新第 1 行" }));
  expect(feedback).toHaveBeenLastCalledWith(expect.stringContaining("retained operation binding\n旧文件路径：未提供\n新文件路径：未提供"));
  expect(feedback.mock.calls[0][0]).not.toContain("possible");
});

it("shows trailing non-hunk file metadata without adding it to another file's quote", async () => {
  const feedback = vi.fn(), suffix = "diff --git a/logo.bin b/logo.bin\nBinary files a/logo.bin and b/logo.bin differ";
  render(<ReviewDiff patch={`${patch}\n${suffix}`} source="versioned source" onFeedback={feedback} />);
  expect(screen.getByText(/Binary files a\/logo.bin and b\/logo.bin differ/)).toBeInTheDocument();
  await userEvent.setup().click(screen.getByRole("button", { name: "引用此片段" }));
  expect(feedback.mock.calls[0][0]).not.toContain("logo.bin");
  expect(feedback.mock.calls[0][0]).toContain("旧文件路径：a/src/app.ts");
});

function render(ui: ReactElement, options?: RenderOptions) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  const Wrapper = options?.wrapper;
  return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
