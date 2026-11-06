import {readFileSync, writeFileSync} from "node:fs";
import {execFileSync} from "node:child_process";
const target = "web/src/v2/components/agent-browser.tsx";
const phase = process.argv[2];
const original = readFileSync("web/.tmp/agent-browser-original.tsx", "utf8");
const next = phase === "base" ? execFileSync("git", ["show", `bab65dca83986f105b16e45e59dfb6676ec16b33:${target}`], {encoding:"utf8"})
  : phase === "after" ? original.replace("本会话的浏览器仅在操作者声明的来源内工作：", "已声明的导航来源：")
    .replace("This session's browser operates only inside the operator-declared origins: ", "Declared navigation origins: ")
  : phase === "current" ? original : null;
if (!next) throw new Error("Unknown phase");
writeFileSync(target, next);
console.log(phase, execFileSync("git", ["hash-object", target], {encoding:"utf8"}).trim());
