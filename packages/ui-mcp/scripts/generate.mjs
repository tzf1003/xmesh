import fs from "node:fs";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
let compile;
try {
  ({ compile } = require("json-schema-to-typescript"));
} catch {
  ({ compile } = createRequire(
    new URL("../../../scripts/gen-control-ts/package.json", import.meta.url),
  )("json-schema-to-typescript"));
}
const schema = JSON.parse(
  fs.readFileSync(
    new URL("../schemas/protocol.schema.json", import.meta.url),
    "utf8",
  ),
);
// Conditional rules are enforced at runtime. The generated types retain the shared wire shape.
const clean = JSON.parse(
  JSON.stringify(schema, (key, value) =>
    ["allOf", "$id"].includes(key) ? undefined : value,
  ),
);
const text = await compile(clean, "UIProtocol", {
  bannerComment:
    "/* Generated from schemas/protocol.schema.json. Run npm run generate. */",
  unreachableDefinitions: true,
});
const out = new URL("../src/protocol.gen.ts", import.meta.url);
if (process.argv.includes("--check")) {
  if (!fs.existsSync(out) || fs.readFileSync(out, "utf8") !== text)
    throw new Error("protocol.gen.ts is stale");
} else fs.writeFileSync(out, text);
