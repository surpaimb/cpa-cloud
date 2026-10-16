import { invoke } from "@tauri-apps/api/core";
import type { ClientConfiguration, ClientDetection } from "./types";

const browserClients: ClientDetection[] = [
  ["claude-code", "Claude Code"],
  ["codex", "Codex CLI"],
  ["gemini", "Gemini CLI"],
  ["claude-desktop", "Claude Desktop"],
  ["grok-build", "Grok Build"],
  ["opencode", "OpenCode"],
  ["openclaw", "OpenClaw"],
  ["hermes", "Hermes"],
  ["pi", "Pi"],
  ["minimax-code", "MiniMax Code"],
].map(([id, name]) => ({
  id: String(id),
  name: String(name),
  detected: false,
  detail: "仅桌面应用可检测本机工具",
}));

const isTauri = () => "__TAURI_INTERNALS__" in window;

export async function detectClients(): Promise<ClientDetection[]> {
  if (!isTauri()) return browserClients;
  return invoke<ClientDetection[]>("detect_clients");
}

export async function discoverConfiguration(
  baseUrl: string,
  employeeKey: string,
): Promise<ClientConfiguration> {
  if (!isTauri()) {
    if (!employeeKey.trim()) throw new Error("员工 Key 不能为空");
    throw new Error("浏览器预览不能验证员工 Key，请使用 CPA 云端配置桌面应用");
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
