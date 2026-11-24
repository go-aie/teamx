#!/usr/bin/env python3
"""Historical reproduction only: patch for the retired AIO 1.11.0 trial.

Run as the container operator, then restart mcp-server-browser. Set
TEAMX_BROWSER_ARTIFACT_ROOT to the one member workspace mount. Upstream tool
schemas and image blocks stay intact; successful screenshots gain a file path.
This is a disposable test-container patch, not a TeamX remote execution adapter.
"""

import hashlib
from pathlib import Path
import shutil


def main():
    launcher = shutil.which("mcp-server-browser")
    if not launcher:
        raise SystemExit("mcp-server-browser not installed")
    target = Path(launcher).resolve()
    source = target.read_text()
    marker = "// TeamX shared-workspace screenshot export"
    if marker in source:
        print("TeamX screenshot export already installed")
        return
    needle = "                const name = (null == args ? void 0 : args.name) ?? 'undefined';\n                resources_screenshots.set(name, screenshot);"
    text_needle = "text: args.fullPage ? `Screenshot of the whole page taken at ${dimensions.width}x${dimensions.height}` : `Screenshot '${name}' taken at ${dimensions.width}x${dimensions.height}`"
    if source.count(needle) != 1 or source.count(text_needle) != 1:
        raise SystemExit("browser MCP differs from tested 1.11.0; refusing to patch")
    export = r'''
                // TeamX shared-workspace screenshot export
                let teamxArtifactText = '';
                if (process.env.TEAMX_BROWSER_ARTIFACT_ROOT) {
                    try {
                        const fs = require('node:fs');
                        const path = require('node:path');
                        const root = fs.realpathSync(process.env.TEAMX_BROWSER_ARTIFACT_ROOT);
                        const directory = path.join(root, 'screenshots');
                        fs.mkdirSync(directory, { recursive: true });
                        if (fs.realpathSync(directory) !== directory) throw Error('screenshot directory must not be a symlink');
                        const data = Buffer.from(screenshot, 'base64');
                        if (data.length < 24 || !data.subarray(0, 8).equals(Buffer.from([137,80,78,71,13,10,26,10]))) throw Error('invalid PNG');
                        if (data.length > 16 * 1024 * 1024 || data.readUInt32BE(16) * data.readUInt32BE(20) > 25000000) throw Error('screenshot exceeds preview budget');
                        const stem = String(args.name || 'browser').replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 60);
                        const filename = `${stem}-${Date.now()}-${require('node:crypto').randomUUID().slice(0, 8)}.png`;
                        fs.writeFileSync(path.join(directory, filename), data, { flag: 'wx', mode: 0o644 });
                        teamxArtifactText = `
Saved workspace file: screenshots/${filename}
Use the relative path in a TeamX message link so the human can preview it.`;
                    } catch (error) {
                        teamxArtifactText = `
Screenshot image generated, but workspace export failed: ${error.message}`;
                    }
                }
'''
    patched = source.replace(needle, needle + export).replace(text_needle, text_needle.replace("text: ", "text: (") + ") + teamxArtifactText")
    before = hashlib.sha256(source.encode()).hexdigest()
    target.write_text(patched)
    print("browser MCP screenshot export installed; before_sha256=" + before + "; after_sha256=" + hashlib.sha256(patched.encode()).hexdigest())


if __name__ == "__main__":
    main()
