// Vite keeps old hashed bundles because emptyOutDir is false. The Go binary
// embeds everything under ui/, so stale copies from previous builds only inflate
// it. Keep exactly the assets referenced by the freshly written index.html.
import {readFileSync, readdirSync, unlinkSync} from "node:fs";
import {dirname, join} from "node:path";
import {fileURLToPath} from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
const ui = join(root, "..", "backend", "cmd", "cloudprint", "ui");
const html = readFileSync(join(ui, "index.html"), "utf8");
const keep = new Set(html.match(/assets\/[A-Za-z0-9._-]+/g) ?? []);
const assetsDir = join(ui, "assets");
let removed = 0;
for (const name of readdirSync(assetsDir)) {
  const rel = `assets/${name}`;
  if (!keep.has(rel)) {
    unlinkSync(join(assetsDir, name));
    removed++;
  }
}
console.log(`pruned ${removed} stale asset(s), kept ${keep.size}`);
