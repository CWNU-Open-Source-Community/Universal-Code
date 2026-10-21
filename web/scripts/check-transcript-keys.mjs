// Guards the strict transcript parser in src/api/client/responses/transcripts.ts against schema
// drift: when docs/openapi.json adds or removes a ThreadTranscriptItemView
// key, the generated schema changes but the hand-written allowlists do not,
// and transcripts silently fail validation in production while mock-based
// unit tests stay green. Compare both sides and fail loudly instead.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const schema = readFileSync(join(root, "src/api/schema.d.ts"), "utf8");
const client = readFileSync(join(root, "src/api/client/responses/transcripts.ts"), "utf8");

function schemaKeys() {
  const start = schema.indexOf("ThreadTranscriptItemView: {");
  if (start < 0) throw new Error("ThreadTranscriptItemView not found in schema.d.ts");
  const body = schema.slice(start);
  const end = body.indexOf("\n        };");
  if (end < 0) throw new Error("ThreadTranscriptItemView block is not closed as expected");
  const required = [];
  const optional = [];
  let keyIndent = "";
  for (const line of body.slice(0, end).split("\n")) {
    const match = line.match(/^(\s+)([a-z_0-9]+)(\?)?:\s/u);
    if (!match) continue;
    // Accept only keys at the nesting level of the first parsed key, so
    // nested object properties are not mistaken for top-level fields.
    if (!keyIndent) keyIndent = match[1];
    if (match[1] !== keyIndent) continue;
    (match[3] ? optional : required).push(match[2]);
  }
  if (required.length === 0) throw new Error("No ThreadTranscriptItemView keys parsed from schema.d.ts");
  return { required, optional };
}

function clientKeys() {
  const start = client.indexOf("function parseThreadTranscriptItem");
  if (start < 0) throw new Error("parseThreadTranscriptItem not found in client/responses/transcripts.ts");
  const body = client.slice(start, start + 2_000);
  const match = body.match(/const required = \[([^\]]*)\];\s*const optional = \[([^\]]*)\];/u);
  if (!match) throw new Error("required/optional allowlists not found in parseThreadTranscriptItem");
  const keys = (list) => [...list.matchAll(/"([a-z_0-9]+)"/gu)].map(([, key]) => key);
  return { required: keys(match[1]), optional: keys(match[2]) };
}

const expected = schemaKeys();
const actual = clientKeys();

const problems = [];
const diff = (from, to, fromLabel, toLabel) => {
  for (const key of from) if (!to.includes(key)) problems.push(`${key} (${fromLabel} 有，${toLabel} 缺)`);
};
diff(expected.required, actual.required, "schema 必填", "client required");
diff(actual.required, expected.required, "client required", "schema 必填");
diff(expected.optional, actual.optional, "schema 可选", "client optional");
diff(actual.optional, expected.optional, "client optional", "schema 可选");
// A key must not switch sides silently: required in one place, optional in the other.
for (const key of actual.required) if (expected.optional.includes(key)) problems.push(`${key} (client 必填，schema 可选)`);
for (const key of actual.optional) if (expected.required.includes(key)) problems.push(`${key} (client 可选，schema 必填)`);

if (problems.length > 0) {
  console.error("ThreadTranscriptItemView 校验名单与 schema.d.ts 不一致：");
  for (const problem of problems) console.error(`  - ${problem}`);
  console.error("请同步 src/api/client/responses/transcripts.ts 中 parseThreadTranscriptItem 的 required/optional 名单。");
  process.exit(1);
}
console.log(`ThreadTranscriptItemView keys OK: ${expected.required.length} required, ${expected.optional.length} optional.`);
