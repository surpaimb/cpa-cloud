import { expect, test } from "vitest";
import { detectClients, discoverConfiguration } from "./api";

test("browser mode never claims local clients were detected", async () => {
  const clients = await detectClients();

  expect(clients).toHaveLength(10);
  expect(clients.every((client) => !client.detected)).toBe(true);
  expect(clients.every((client) => client.detail === "仅桌面应用可检测本机工具")).toBe(true);
});

test("browser mode fails closed instead of simulating employee-key authentication", async () => {
  await expect(
    discoverConfiguration("https://cpa.company.example", "synthetic-employee-key"),
  ).rejects.toThrow("浏览器预览不能验证员工 Key，请使用 CPA 云端配置桌面应用");
});
