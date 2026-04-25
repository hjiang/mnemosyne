// Auto-format Go files after Edit/Write tool calls.
//
// Runs `goimports -w` on the touched file when available (also fixes import
// ordering); falls back to `gofmt -w` otherwise. Failures are swallowed so a
// missing formatter does not break the tool result the LLM sees — the
// project's lint check (`golangci-lint run`) is the authoritative gate.

import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { spawnSync } from "node:child_process";

function formatGo(absPath: string): void {
  // Try goimports first (superset of gofmt: also rewrites imports).
  const gi = spawnSync("goimports", ["-w", absPath], { stdio: "ignore" });
  if (gi.status === 0) return;
  // Fall back to gofmt if goimports is unavailable or failed.
  spawnSync("gofmt", ["-w", absPath], { stdio: "ignore" });
}

export default function (pi: ExtensionAPI) {
  pi.on("tool_result", async (event, _ctx) => {
    if (event.isError) return;
    const name = String(event.toolName ?? "").toLowerCase();
    if (name !== "edit" && name !== "write") return;

    // Edit/Write inputs use `path` in pi's built-in tool schema.
    const input = event.input as { path?: string } | undefined;
    const path = input?.path;
    if (typeof path !== "string" || !path.endsWith(".go")) return;

    try {
      formatGo(path);
    } catch {
      // Never let the formatter affect tool execution.
    }
  });
}
