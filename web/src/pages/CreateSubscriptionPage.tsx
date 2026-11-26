import { useEffect, useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, errorMessage, formatBytes } from "../api";
import { Layout, PageHeading } from "../components/Layout";
import type { Subscription, User, UserConfig } from "../types";

export function CreateSubscriptionPage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const navigate = useNavigate();
  const [config, setConfig] = useState<UserConfig | null>(null);
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [contentType, setContentType] = useState<Subscription["contentType"]>("text/yaml");
  const [base64Encoded, setBase64Encoded] = useState(false);
  const [neverExpires, setNeverExpires] = useState(false);
  const [validityHours, setValidityHours] = useState(24);
  const [unlimitedRequests, setUnlimitedRequests] = useState(false);
  const [maxRequests, setMaxRequests] = useState(100);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<Subscription | null>(null);

  useEffect(() => {
    api.config().then(({ config: value }) => {
      setConfig(value);
      setValidityHours(Math.round(value.defaultLinkValiditySeconds / 3600));
    }).catch((err) => setError(errorMessage(err)));
  }, []);

  async function importFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !config) return;
    setError("");
    try {
      const extension = file.name.split(".").pop()?.toLowerCase() || "";
      if (!["yaml", "yml", "txt"].includes(extension)) throw new Error("仅支持 .yaml、.yml 或 .txt 文件");
      if (file.size > config.maxTextBytes) throw new Error(`文件超过 ${formatBytes(config.maxTextBytes)}`);
      const value = await file.text();
      if (!value.trim()) throw new Error("文件中没有可导入的内容");
      setContent(value);
      setContentType(extension === "txt" ? "text/plain" : "text/yaml");
      if (!title.trim()) setTitle(file.name.replace(/\.(yaml|yml|txt)$/i, ""));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!config) return;
    setError("");
    setBusy(true);
    try {
      const bytes = new TextEncoder().encode(content).length;
      if (!content.trim()) throw new Error("订阅内容不能为空");
      if (bytes > config.maxTextBytes) throw new Error(`内容超过 ${formatBytes(config.maxTextBytes)}`);
      const validitySeconds = neverExpires ? 0 : Math.round(validityHours * 3600);
      if (!neverExpires && (validitySeconds < 60 || validitySeconds > config.maxLinkValiditySeconds)) {
        throw new Error(`有效期必须在 1 分钟到 ${Math.floor(config.maxLinkValiditySeconds / 3600)} 小时之间`);
      }
      if (!unlimitedRequests && (!Number.isInteger(maxRequests) || maxRequests < 1)) {
        throw new Error("请求上限必须是大于 0 的整数");
      }
      const result = await api.createSubscription({
        title,
        content,
        contentType,
        base64Encoded,
        linkValiditySeconds: validitySeconds,
        maxRequests: unlimitedRequests ? 0 : maxRequests,
      });
      setCreated(result.subscription);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  if (created) {
    return (
      <Layout user={user} onLogout={onLogout}>
        <section className="created-card">
          <div className="success-orb">✓</div>
          <div className="eyebrow">SUBSCRIPTION READY</div>
          <h1>订阅链接已创建</h1>
          <p>客户端访问时直接输出内容。成功请求次数只用于计数，不建立浏览器领取会话。</p>
          <div className="secret-link">{created.publicLink}</div>
          <div className="button-row centered">
            <button className="button primary" onClick={() => void navigator.clipboard.writeText(created.publicLink || "")}>复制订阅链接</button>
            <button className="button secondary" onClick={() => navigate("/")}>返回控制台</button>
          </div>
          <p className="fine-print">Token 位于 URL 路径中。应用日志会脱敏，但外部反向代理仍应避免记录完整访问路径。</p>
        </section>
      </Layout>
    );
  }

  return (
    <Layout user={user} onLogout={onLogout} wide>
      <PageHeading
        eyebrow="DIRECT SUBSCRIPTION"
        title="创建订阅链接"
        description="Clash、Base64 或其它文本内容会按请求直接输出，不分配标签页会话。达到上限或过期后可在控制台重新启用。"
        actions={<Link className="button ghost" to="/">取消</Link>}
      />
      <form onSubmit={submit} className="create-form">
        <section className="panel form-section">
          <div className="section-number">01</div>
          <div className="section-content">
            <h2>基本设置</h2>
            <div className="form-grid two">
              <label className="span-two">订阅标题<input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={100} placeholder="例如：临时 Clash 订阅" required /></label>
              <label>输出类型<select value={contentType} onChange={(e) => setContentType(e.target.value as Subscription["contentType"])}><option value="text/yaml">YAML / Clash</option><option value="text/plain">纯文本</option><option value="application/octet-stream">二进制原始内容</option></select></label>
              <label className="toggle-row compact"><input type="checkbox" checked={base64Encoded} onChange={(e) => setBase64Encoded(e.target.checked)} /><span><strong>输出前进行 Base64 编码</strong><small>启用后响应类型固定为 text/plain。</small></span></label>
              <label>请求上限<input type="number" min={1} step={1} value={maxRequests} onChange={(e) => setMaxRequests(Number(e.target.value))} disabled={unlimitedRequests} required={!unlimitedRequests} /></label>
              <label className="toggle-row compact"><input type="checkbox" checked={unlimitedRequests} onChange={(e) => setUnlimitedRequests(e.target.checked)} /><span><strong>无限请求次数</strong><small>仍然统计成功请求次数。</small></span></label>
              <label>有效期（小时）<input type="number" min={1 / 60} max={(config?.maxLinkValiditySeconds || 86400) / 3600} step={1 / 60} value={validityHours} onChange={(e) => setValidityHours(Number(e.target.value))} disabled={neverExpires} required={!neverExpires} /></label>
              <label className="toggle-row compact"><input type="checkbox" checked={neverExpires} onChange={(e) => setNeverExpires(e.target.checked)} /><span><strong>永不过期</strong><small>只能手动删除或达到请求上限后停用。</small></span></label>
            </div>
          </div>
        </section>
        <section className="panel form-section">
          <div className="section-number">02</div>
          <div className="section-content">
            <div className="section-title-line">
              <div><h2>订阅内容</h2><p>内容原样加密保存。导入文件会覆盖当前编辑器内容。</p></div>
              <label className="button secondary small file-button">导入 YAML / TXT<input type="file" accept=".yaml,.yml,.txt,text/plain,text/yaml,application/yaml" onChange={(e) => void importFile(e)} /></label>
            </div>
            <label>内容<textarea className="subscription-editor" rows={22} value={content} onChange={(e) => setContent(e.target.value)} placeholder="粘贴 Clash YAML、Base64 或其它订阅文本…" required /><small>{formatBytes(new TextEncoder().encode(content).length)} / {formatBytes(config?.maxTextBytes || 0)}</small></label>
          </div>
        </section>
        {error && <div className="alert error">{error}</div>}
        <div className="submit-bar">
          <div><strong>直接输出订阅</strong><span>每次成功响应计为一次请求</span></div>
          <button className="button primary large" disabled={busy || !config}>{busy ? "正在加密并创建…" : "创建订阅链接"}</button>
        </div>
      </form>
    </Layout>
  );
}
