import { invoke } from "@tauri-apps/api/core";
import type { ClientConfiguration, ClientDetection } from "./types";

const demoClients: ClientDetection[] = [
  ["claude-code", "Claude Code", true],
  ["codex", "Codex CLI", true],
  ["gemini", "Gemini CLI", true],
  ["claude-desktop", "Claude Desktop", true],
  ["grok-build", "Grok Build", true],
  ["opencode", "OpenCode", true],
  ["openclaw", "OpenClaw", true],
  ["hermes", "Hermes", true],
  ["pi", "Pi", false],
  ["minimax-code", "MiniMax Code", false],
].map(([id, name, detected]) => ({
  id: String(id),
  name: String(name),
  detected: Boolean(detected),
  detail: detected ? "已检测到本机配置目录" : "未检测到本机安装",
}));

const isTauri = () => "__TAURI_INTERNALS__" in window;

export async function detectClients(): Promise<ClientDetection[]> {
  if (!isTauri()) return demoClients;
  return invoke<ClientDetection[]>("detect_clients");
}

export async function discoverConfiguration(
  baseUrl: string,
  employeeKey: string,
): Promise<ClientConfiguration> {
  if (!isTauri()) {
    if (!employeeKey.trim()) throw new Error("员工 Key 不能为空");
    await new Promise((resolve) => window.setTimeout(resolve, 260));
    return {
      schema_version: 1,
      protocols: [
        { protocol: "anthropic-messages", candidates: ["claude-sonnet"], default_model: "claude-sonnet", default_source: "public_id_ascending" },
        { protocol: "openai-responses", candidates: ["gpt-codex"], default_model: "gpt-codex", default_source: "public_id_ascending" },
        { protocol: "gemini-generate-content", candidates: ["gemini-pro"], default_model: "gemini-pro", default_source: "public_id_ascending" },
        { protocol: "openai-chat", candidates: ["general-chat"], default_model: "general-chat", default_source: "public_id_ascending" },
      ],
    };
  }
  return invoke<ClientConfiguration>("discover_configuration", {
    request: { base_url: baseUrl, employee_key: employeeKey },
  });
}

export async function applyConfiguration(baseUrl: string, employeeKey: string): Promise<void> {
  if (!isTauri()) {
    throw new Error("浏览器预览不会写入本机配置");
  }
  await invoke("apply_configuration", {
    request: { base_url: baseUrl, employee_key: employeeKey },
  });
}
