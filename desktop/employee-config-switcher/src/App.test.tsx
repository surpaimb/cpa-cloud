import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import App from "./App";

vi.mock("./api", () => ({
  detectClients: vi.fn().mockResolvedValue([
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
    detail: detected ? "显式测试 fixture：已检测" : "显式测试 fixture：未检测",
  }))),
  discoverConfiguration: vi.fn(async (_baseUrl: string, employeeKey: string) => {
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
  }),
}));

test("keeps employee key masked and exposes the ten-tool workflow", async () => {
  const user = userEvent.setup();
  render(<App />);
  const key = screen.getByLabelText("员工 Key") as HTMLInputElement;
  expect(key.type).toBe("password");
  await user.type(key, "demo-key");
  await user.click(screen.getByRole("button", { name: "显示员工 Key" }));
  expect(key.type).toBe("text");
  expect(await screen.findByText("已检测到 8 个工具")).toBeInTheDocument();
  expect(screen.getByText("MiniMax Code")).toBeInTheDocument();
});

test("discards an in-flight discovery result after either input changes", async () => {
  const user = userEvent.setup();
  render(<App />);
  const key = screen.getByLabelText("员工 Key");
  await user.type(key, "first-key");
  await user.click(screen.getByRole("button", { name: "检测连接" }));
  await user.type(key, "-changed");

  expect(screen.getByText("员工 Key 已更改，请重新检测连接。")).toBeInTheDocument();
  await new Promise((resolve) => window.setTimeout(resolve, 320));
  expect(screen.queryByText(/连接成功/)).not.toBeInTheDocument();
  expect(screen.getByText("开发预览")).toBeInTheDocument();
});

test("does not claim detected tools are configurable after discovery", async () => {
  const user = userEvent.setup();
  render(<App />);
  await user.type(screen.getByLabelText("员工 Key"), "demo-key");
  await user.click(screen.getByRole("button", { name: "检测连接" }));

  expect(await screen.findByText(/连接成功/)).toBeInTheDocument();
  expect(screen.getAllByText("已检测，适配未就绪")).not.toHaveLength(0);
  expect(screen.queryByText("可配置")).not.toBeInTheDocument();
});
