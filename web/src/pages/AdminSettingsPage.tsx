import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, formatBytes } from "../api";
import { Layout, PageHeading } from "../components/Layout";
import type { Settings, User } from "../types";

export function AdminSettingsPage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [extensions, setExtensions] = useState("");
  const [mimeMap, setMimeMap] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.settings().then(({ settings: value }) => {
      setSettings(value);
      setExtensions(value.allowedExtensions.join(", "));
      setMimeMap(Object.entries(value.mimeMap).map(([ext, mime]) => `${ext}=${mime}`).join("\n"));
    }).catch((err) => setError(errorMessage(err)));
  }, []);

  function patch<K extends keyof Settings>(key: K, value: Settings[K]) {
    setSaved(false);
    setSettings((current) => current ? { ...current, [key]: value } : current);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!settings) return;
    setBusy(true);
    setError("");
    try {
      const allowedExtensions = extensions.split(/[,\s]+/).map((v) => v.trim().replace(/^\./, "")).filter(Boolean);
      const allowed = new Set(allowedExtensions);
      const nextMime: Record<string, string> = {};
      for (const line of mimeMap.split("\n")) {
        const at = line.indexOf("=");
        if (at < 1) continue;
        const ext = line.slice(0, at).trim().replace(/^\./, "").toLowerCase();
        if (allowed.has(ext)) nextMime[ext] = line.slice(at + 1).trim();
      }
      const result = await api.saveSettings({ ...settings, allowedExtensions, mimeMap: nextMime });
      setSettings(result.settings);
      setSaved(true);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  if (!settings) return <Layout user={user} onLogout={onLogout}><div className="panel">正在读取设置…</div></Layout>;

  return (
    <Layout user={user} onLogout={onLogout} wide>
      <PageHeading eyebrow="SYSTEM LIMITS" title="全局设置" description="新限制只用于新分享；已创建分享保留创建时的长度和内容政策。缩小白名单等网络规则由每条分享独立配置。" />
      <form onSubmit={submit} className="settings-form">
        <section className="panel form-section">
          <div className="section-number">01</div><div className="section-content"><h2>容量与数量</h2><div className="form-grid two">
            <NumberField label="单文件上限（MiB）" value={settings.maxFileBytes / 1024 / 1024} onChange={(v) => patch("maxFileBytes", Math.round(v * 1024 * 1024))} min={1} max={1024} />
            <NumberField label="总存储配额（MiB）" value={settings.maxTotalStorageBytes / 1024 / 1024} onChange={(v) => patch("maxTotalStorageBytes", Math.round(v * 1024 * 1024))} min={10} />
            <NumberField label="每用户活跃链接数" value={settings.maxActiveSharesPerUser} onChange={(v) => patch("maxActiveSharesPerUser", v)} min={1} max={100} />
            <NumberField label="每条链接内容份数" value={settings.maxItemsPerShare} onChange={(v) => patch("maxItemsPerShare", v)} min={1} max={1000} />
            <NumberField label="每份文本上限（KiB）" value={settings.maxTextBytes / 1024} onChange={(v) => patch("maxTextBytes", Math.round(v * 1024))} min={1} max={2048} />
            <NumberField label="表格最大列数" value={settings.maxColumns} onChange={(v) => patch("maxColumns", v)} min={1} max={100} />
          </div></div>
        </section>
        <section className="panel form-section">
          <div className="section-number">02</div><div className="section-content"><h2>时长上限</h2><div className="form-grid two">
            <NumberField label="最长会话（分钟）" value={settings.maxSessionTimeoutSeconds / 60} onChange={(v) => patch("maxSessionTimeoutSeconds", Math.round(v * 60))} min={0.5} />
            <NumberField label="默认会话（分钟）" value={settings.defaultSessionTimeoutSeconds / 60} onChange={(v) => patch("defaultSessionTimeoutSeconds", Math.round(v * 60))} min={0.5} />
            <NumberField label="最长链接有效期（小时）" value={settings.maxLinkValiditySeconds / 3600} onChange={(v) => patch("maxLinkValiditySeconds", Math.round(v * 3600))} min={1} />
            <NumberField label="默认链接有效期（小时）" value={settings.defaultLinkValiditySeconds / 3600} onChange={(v) => patch("defaultLinkValiditySeconds", Math.round(v * 3600))} min={1} />
          </div></div>
        </section>
        <section className="panel form-section">
          <div className="section-number">03</div><div className="section-content"><h2>文件类型与 MIME</h2><p>MIME 映射只描述展示和下载类型。HTML、SVG、脚本等危险内容始终强制作为附件下载。</p><div className="form-grid two">
            <label>允许扩展名<input value={extensions} onChange={(e) => { setSaved(false); setExtensions(e.target.value); }} /><small>逗号或空格分隔，例如 png, jpg, pdf, zip</small></label>
            <label>MIME 映射<textarea rows={9} value={mimeMap} onChange={(e) => { setSaved(false); setMimeMap(e.target.value); }} /><small>每行“扩展名=类型”，未配置项使用系统映射。</small></label>
          </div><div className="config-hint wide"><span>当前单文件上限</span><strong>{formatBytes(settings.maxFileBytes)}</strong><span>总存储 {formatBytes(settings.maxTotalStorageBytes)}</span></div></div>
        </section>
        <section className="panel form-section">
          <div className="section-number">04</div><div className="section-content">
            <h2>Cloudflare Turnstile</h2>
            <p>开启后，接收者必须先完成 Cloudflare 人机验证，服务端才会分配一次性内容。私密密钥只加密保存在服务器，后台不会回显。</p>
            <label className="toggle-row"><input type="checkbox" checked={settings.turnstileEnabled} onChange={(e) => patch("turnstileEnabled", e.target.checked)} /><span><strong>在分享领取页启用 Turnstile</strong><small>关闭后保留已配置密钥，方便以后重新启用。</small></span></label>
            <div className="form-grid two turnstile-fields">
              <label>站点密钥（Site Key）<input value={settings.turnstileSiteKey} onChange={(e) => patch("turnstileSiteKey", e.target.value)} placeholder="0x4AAAAAAA..." autoComplete="off" /></label>
              <label>私密密钥（Secret Key）<input type="password" disabled={Boolean(settings.turnstileClearSecret)} value={settings.turnstileSecretKey || ""} onChange={(e) => patch("turnstileSecretKey", e.target.value)} placeholder={settings.turnstileSecretConfigured ? "已配置，留空保持不变" : "输入 Cloudflare Secret Key"} autoComplete="new-password" /><small>{settings.turnstileSecretConfigured ? "已保存加密密钥。输入新值可替换，留空则保持不变。" : "密钥不会通过接口回显。"}</small></label>
            </div>
            {settings.turnstileSecretConfigured && <label className="toggle-row compact"><input type="checkbox" checked={Boolean(settings.turnstileClearSecret)} onChange={(e) => patch("turnstileClearSecret", e.target.checked)} /><span><strong>清除已保存的私密密钥</strong><small>启用 Turnstile 时不能清除密钥。</small></span></label>}
          </div>
        </section>
        {error && <div className="alert error">{error}</div>}
        {saved && <div className="alert success">设置已保存。</div>}
        <div className="submit-bar"><div><strong>全局限制</strong><span>调整立即影响新的分享配置</span></div><button className="button primary large" disabled={busy}>{busy ? "保存中…" : "保存设置"}</button></div>
      </form>
    </Layout>
  );
}

function NumberField({ label, value, onChange, min, max }: { label: string; value: number; onChange: (value: number) => void; min?: number; max?: number }) {
  return <label>{label}<input type="number" value={Number.isFinite(value) ? value : 0} min={min} max={max} step="any" onChange={(e) => onChange(Number(e.target.value))} required /></label>;
}

