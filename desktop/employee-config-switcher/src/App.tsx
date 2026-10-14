import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { detectClients, discoverConfiguration } from "./api";
import type { AppPhase, ClientConfiguration, ClientDetection } from "./types";
import "./styles.css";

function EyeIcon({ open }: { open: boolean }) {
  return open ? (
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><circle cx="12" cy="12" r="2.7"/></svg>
  ) : (
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m3 3 18 18M10.6 6.2A10.8 10.8 0 0 1 12 6c6 0 9.5 6 9.5 6a14.5 14.5 0 0 1-2.2 3M6.5 6.7C3.9 8.5 2.5 12 2.5 12S6 18 12 18a9.8 9.8 0 0 0 3.3-.6M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>
  );
}

function statusText(client: ClientDetection, phase: AppPhase) {
  if (!client.detected) return { tone: "warning", label: "未检测到", detail: client.detail };
  if (phase === "blocked") return { tone: "neutral", label: "已检测", detail: "安全写入尚未开放" };
  if (phase === "ready") return { tone: "ready", label: "可配置", detail: "已确认服务端权限" };
  return { tone: "neutral", label: "已检测", detail: client.detail };
}

export default function App() {
  const [baseUrl, setBaseUrl] = useState("https://cpa.company.com");
  const [employeeKey, setEmployeeKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const [clients, setClients] = useState<ClientDetection[]>([]);
  const [configuration, setConfiguration] = useState<ClientConfiguration | null>(null);
  const [phase, setPhase] = useState<AppPhase>("idle");
  const [message, setMessage] = useState("输入公司提供的地址和员工 Key，然后检测连接。");
  const inputGeneration = useRef(0);

  useEffect(() => {
    detectClients().then(setClients).catch(() => setClients([]));
  }, []);

  const detectedCount = useMemo(() => clients.filter((client) => client.detected).length, [clients]);
  const modelCount = useMemo(
    () => configuration?.protocols.reduce((total, item) => total + item.candidates.length, 0) ?? 0,
    [configuration],
  );

  async function checkConnection(event?: FormEvent) {
    event?.preventDefault();
    const generation = inputGeneration.current;
    const requestedUrl = baseUrl;
    const requestedKey = employeeKey;
    setConfiguration(null);
    setPhase("checking");
    setMessage("正在验证员工 Key 和可用模型…");
    try {
      const result = await discoverConfiguration(requestedUrl, requestedKey);
      if (generation !== inputGeneration.current) return;
      const count = result.protocols.reduce((total, item) => total + item.candidates.length, 0);
      setConfiguration(result);
      setPhase("ready");
      setMessage(`连接成功，当前 Key 可使用 ${count} 个协议模型。`);
    } catch (error) {
      if (generation !== inputGeneration.current) return;
      setConfiguration(null);
      setPhase("idle");
      setMessage(error instanceof Error ? error.message : "连接失败，请检查地址和员工 Key。");
    }
  }

  function invalidateDiscovery(nextMessage: string) {
    inputGeneration.current += 1;
    setConfiguration(null);
    setPhase("idle");
    setMessage(nextMessage);
  }

  return (
    <main className="app-shell">
      <div className="titlebar-drag" aria-hidden="true" />
      <section className="workspace" aria-labelledby="app-title">
        <header className="masthead">
          <div className="brand-mark" aria-hidden="true"><span /></div>
          <div>
            <h1 id="app-title">CPA 云端配置</h1>
            <p>把公司授权安全写入你已安装的 AI 工具</p>
          </div>
          <div className={`phase phase-${phase}`} aria-live="polite">
            <span />{phase === "ready" ? "连接正常" : phase === "checking" ? "检测中" : "开发预览"}
          </div>
        </header>

        <form className="connection-form" onSubmit={checkConnection}>
          <label htmlFor="base-url">CPA 地址</label>
          <input
            id="base-url"
            value={baseUrl}
            onChange={(event) => {
              setBaseUrl(event.target.value);
              invalidateDiscovery("地址已更改，请重新检测连接。");
            }}
            inputMode="url"
            autoComplete="url"
            spellCheck={false}
            placeholder="https://cpa.company.com"
          />

          <label htmlFor="employee-key">员工 Key</label>
          <div className="secret-field">
            <input
              id="employee-key"
              type={showKey ? "text" : "password"}
              value={employeeKey}
              onChange={(event) => {
                setEmployeeKey(event.target.value);
                invalidateDiscovery("员工 Key 已更改，请重新检测连接。");
              }}
              autoComplete="off"
              spellCheck={false}
              placeholder="输入公司提供的员工 Key"
            />
            <button type="button" className="icon-button" onClick={() => setShowKey((value) => !value)} aria-label={showKey ? "隐藏员工 Key" : "显示员工 Key"}>
              <EyeIcon open={showKey} />
            </button>
          </div>

          <div className="actions">
            <button className="secondary" type="submit" disabled={phase === "checking" || phase === "applying"}>检测连接</button>
            <button className="primary" type="button" disabled>一键配置全部（待安全审查）</button>
          </div>
        </form>

        <section className="client-section" aria-labelledby="client-title">
          <div className="section-heading">
            <div>
              <h2 id="client-title">已检测到 {detectedCount} 个工具</h2>
              <p>{configuration ? `服务端返回 ${modelCount} 个可用协议模型` : "只显示本机检测结果，不会读取或上传现有配置内容"}</p>
            </div>
            <span className="counter">{clients.length || 10} 项</span>
          </div>

          <div className="client-list" role="table" aria-label="支持的客户端">
            <div className="list-head" role="row">
              <span role="columnheader">工具名称</span>
              <span role="columnheader">状态</span>
              <span role="columnheader">说明</span>
            </div>
            {clients.map((client) => {
              const status = statusText(client, phase);
              return (
                <div className="client-row" role="row" key={client.id}>
                  <strong role="cell">{client.name}</strong>
                  <span className={`status status-${status.tone}`} role="cell"><i />{status.label}</span>
                  <span className="detail" role="cell" title={status.detail}>{status.detail}</span>
                </div>
              );
            })}
          </div>
        </section>

        <footer className="safety-footer">
          <div className="safety-copy">
            <span className="info-icon" aria-hidden="true">i</span>
            <div><strong>当前开发预览只检测，不写入真实配置</strong><p>{message}</p></div>
          </div>
          <span className="privacy-note">正式启用后，Key 会保存到目标工具要求的位置</span>
        </footer>
      </section>
    </main>
  );
}
