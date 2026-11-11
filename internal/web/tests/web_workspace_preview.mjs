// Generate an isolated static browser preview. Serve the printed directory with
// python3 -m http.server PORT --bind 127.0.0.1 --directory DIRECTORY.
import { mkdir, copyFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
const destination = resolve(process.argv[2] || ".local/workspace-ui-preview");
await mkdir(`${destination}/ui/vendor`, { recursive: true });
for (const file of ["app.js", "style.css", "markdown.mjs", "vendor/marked.mjs"]) await copyFile(new URL(`../assets/${file}`, import.meta.url), `${destination}/ui/${file}`);
await copyFile(new URL("../assets/index.html", import.meta.url), `${destination}/index.html`);
await copyFile(new URL("../assets/client.mjs", import.meta.url), `${destination}/ui/client-base.mjs`);
await copyFile(new URL("./web_workspace_fixture.mjs", import.meta.url), `${destination}/ui/workspace-fixture.mjs`);
await writeFile(`${destination}/ui/client.mjs`, 'export * from "./client-base.mjs";\nexport { fixtureRequest as request } from "./workspace-fixture.mjs";\n');
console.log(destination);
