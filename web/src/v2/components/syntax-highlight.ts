import hljs from "highlight.js/lib/core";
import javascript from "highlight.js/lib/languages/javascript";
import typescript from "highlight.js/lib/languages/typescript";
import python from "highlight.js/lib/languages/python";
import go from "highlight.js/lib/languages/go";
import rust from "highlight.js/lib/languages/rust";
import bash from "highlight.js/lib/languages/bash";
import powershell from "highlight.js/lib/languages/powershell";
import json from "highlight.js/lib/languages/json";
import css from "highlight.js/lib/languages/css";
import xml from "highlight.js/lib/languages/xml";
import sql from "highlight.js/lib/languages/sql";
import yaml from "highlight.js/lib/languages/yaml";
import diff from "highlight.js/lib/languages/diff";

for (const [name, language] of Object.entries({ javascript, typescript, python, go, rust, bash, powershell, json, css, xml, sql, yaml, diff })) {
  hljs.registerLanguage(name, language);
}
hljs.registerAliases(["shell", "sh", "zsh"], { languageName: "bash" });
hljs.registerAliases("tsx", { languageName: "typescript" });
hljs.registerAliases("jsx", { languageName: "javascript" });

// Only explicit, supported languages are parsed. Unknown and large blocks
// remain plain, selectable code; do not run automatic language detection.
export function highlightCode(code: string, language: string): string | undefined {
  if (code.length > 50_000 || !hljs.getLanguage(language)) return undefined;
  return hljs.highlight(code, { language, ignoreIllegals: true }).value;
}
