import { completedFilePath, filePathMention, plainFilePathMention } from "./file-path-mention";

it("splits only the current path into a parent directory and a filename prefix", () => {
  expect(filePathMention("检查 @", 4)).toMatchObject({ from: 3, parent: ".", prefix: "" });
  expect(filePathMention("检查 @src/v2/co 后文", 13)).toMatchObject({ from: 3, to: 13, parent: "src/v2", prefix: "co" });
  expect(filePathMention("@src/", 5)).toMatchObject({ parent: "src", prefix: "" });
  expect(filePathMention("@.", 2)).toMatchObject({ parent: ".", prefix: "." });
});

it("keeps spaces, Unicode and escaped quotes in paths without changing the server path", () => {
  const directory = completedFilePath("资料 目录", true);
  expect(directory).toEqual({ text: '@"资料 目录/"', caret: 8 });
  expect(filePathMention(directory.text, directory.caret)).toMatchObject({ parent: "资料 目录", prefix: "", to: 9 });
  expect(filePathMention('@"资料 目录/说明 文', 12)).toMatchObject({ parent: "资料 目录", prefix: "说明 文" });
  const quote = completedFilePath('has"quote', true);
  expect(filePathMention(quote.text, quote.caret)).toMatchObject({ parent: 'has"quote' });
  expect(completedFilePath("资料 目录/说明 文.md", false).text).toBe('@"资料 目录/说明 文.md" ');
});

it.each(["@src/component.ts", '@"资料 目录/说明 文.md"'])
  ("does not complete inside the existing path %s", (text) => {
    expect(filePathMention(text, text.indexOf("/") + 2)).toBeNull();
  });

it.each(["name@example.com", "@../private", "@/root/file", "@C:/file", "@src\\file", "@src//file", "@./file", "@read me", '@"closed"'])
  ("does not interpret %s as an active project path", (text) => expect(filePathMention(text, text.length)).toBeNull());

it.each(["```ts\n@src", "~~~text\n@src", "    @src", "- `@src", "> ```\n> @src", "`unfinished @src", "[label @src](target)"])
  ("protects Markdown code/link text: %s", (text) => {
    const caret = text.indexOf("@src") + 4;
    expect(plainFilePathMention(text, caret)).toBeNull();
  });

it("allows normal text after a completed code span or a closed fence", () => {
  for (const text of ["`code` @src", "```\ncode\n```\n@src", "- Review @src"]) {
    expect(plainFilePathMention(text, text.length)).toMatchObject({ parent: ".", prefix: "src" });
  }
});
