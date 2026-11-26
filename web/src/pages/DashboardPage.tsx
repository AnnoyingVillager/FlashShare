import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage, formatBytes } from "../api";
import { EmptyState, Layout, PageHeading } from "../components/Layout";
import type { Share, Subscription, User, UserConfig } from "../types";

type SubscriptionEditState = {
  id: string;
  content: string;
  contentType: Subscription["contentType"];
  base64Encoded: boolean;
  remainingRequests: number;
  unlimitedRequests: boolean;
  validityHours: number;
  neverExpires: boolean;
  successfulRequests: number;
  active: boolean;
};

export function DashboardPage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [shares, setShares] = useState<Share[]>([]);
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([]);
  const [config, setConfig] = useState<UserConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const [editState, setEditState] = useState<SubscriptionEditState | null>(null);
  const [editLoadingId, setEditLoadingId] = useState("");
  const [editSaving, setEditSaving] = useState(false);

  async function load() {
    setLoading(true);
    setError("");
    try {
      const [shareResult, subscriptionResult, configResult] = await Promise.all([
        api.shares(), api.subscriptions(), api.config()
      ]);
      setShares(shareResult.shares);
      setSubscriptions(subscriptionResult.subscriptions);
      setConfig(configResult.config);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  async function copyLink(key: string, link?: string) {
    if (!link) return;
    await navigator.clipboard.writeText(link);
    setCopied(key);
    window.setTimeout(() => setCopied(""), 1800);
  }

  async function destroyShare(share: Share) {
    if (!window.confirm(`立即销毁“${share.title}”的所有未领取和已领取内容？`)) return;
    try {
      await api.deleteShare(share.id);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function destroySubscription(subscription: Subscription) {
    if (!window.confirm(`永久删除订阅“${subscription.title}”及其内容？`)) return;
    try {
      await api.deleteSubscription(subscription.id);
      if (editState?.id === subscription.id) setEditState(null);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function toggleEdit(subscription: Subscription) {
    if (editState?.id === subscription.id) {
      setEditState(null);
      return;
    }
    setEditLoadingId(subscription.id);
    setEditState(null);
    setError("");
    try {
      const result = await api.subscription(subscription.id);
      const current = result.subscription;
      const now = Date.now() / 1000;
      const maxValidityHours = (config?.maxLinkValiditySeconds || 86400) / 3600;
      const currentRemainingHours = current.expiresAt > now
        ? (current.expiresAt - now) / 3600
        : (config?.defaultLinkValiditySeconds || 86400) / 3600;
      setEditState({
        id: current.id,
        content: result.content,
        contentType: current.contentType,
        base64Encoded: current.base64Encoded,
        remainingRequests: current.maxRequests === 0
          ? 100
          : Math.max(1, current.maxRequests - current.successfulRequests),
        unlimitedRequests: current.maxRequests === 0,
        validityHours: Math.min(maxValidityHours, Math.max(1 / 60, currentRemainingHours)),
        neverExpires: current.expiresAt === 0,
        successfulRequests: current.successfulRequests,
        active: (current.expiresAt === 0 || current.expiresAt > now)
          && (current.maxRequests === 0 || current.successfulRequests < current.maxRequests),
      });
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setEditLoadingId("");
    }
  }

  async function saveSubscription(event: FormEvent) {
    event.preventDefault();
    if (!editState || !config) return;
    const contentBytes = new TextEncoder().encode(editState.content).length;
    if (!editState.content.trim()) {
      setError("订阅内容不能为空");
      return;
    }
    if (contentBytes > config.maxTextBytes) {
      setError(`订阅内容超过 ${formatBytes(config.maxTextBytes)}`);
      return;
    }
    const validitySeconds = editState.neverExpires ? 0 : Math.round(editState.validityHours * 3600);
    if (!editState.neverExpires && (validitySeconds < 60 || validitySeconds > config.maxLinkValiditySeconds)) {
      setError(`有效期必须在 1 分钟到 ${Math.floor(config.maxLinkValiditySeconds / 3600)} 小时之间`);
      return;
    }
    if (!editState.unlimitedRequests && (!Number.isInteger(editState.remainingRequests) || editState.remainingRequests < 1)) {
      setError("剩余请求次数必须是大于 0 的整数");
      return;
    }
    setEditSaving(true);
    setError("");
    try {
      await api.updateSubscription(editState.id, {
        content: editState.content,
        linkValiditySeconds: validitySeconds,
        remainingRequests: editState.unlimitedRequests ? 0 : editState.remainingRequests,
        unlimitedRequests: editState.unlimitedRequests,
      });
      setEditState(null);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setEditSaving(false);
    }
  }

  const activeSubscriptionRequests = subscriptions.reduce((sum, item) => sum + item.successfulRequests, 0);

  return (
    <Layout user={user} onLogout={onLogout} wide>
      <PageHeading
        eyebrow="WELCOME BACK"
        title="分享控制台"
        description="限时分享按标签页领取；订阅链接直接输出配置文本并按成功请求计数。"
        actions={<div className="button-row">
          <Link className="button secondary" to="/subscriptions/new">＋ 新建订阅链接</Link>
          <Link className="button primary" to="/shares/new">＋ 新建分享</Link>
        </div>}
      />
      <div className="metric-row">
        <Metric label="分享链接" value={shares.length} />
        <Metric label="可领取内容" value={shares.reduce((sum, item) => sum + item.availableCount, 0)} />
        <Metric label="订阅链接" value={subscriptions.length} />
        <Metric label="成功订阅请求" value={activeSubscriptionRequests} />
      </div>
      {error && <div className="alert error">{error}</div>}
      {loading ? <div className="panel loading-panel">正在读取链接…</div> : (
        <>
          <h2 className="list-heading">限时分享</h2>
          {shares.length === 0 ? (
            <EmptyState title="还没有分享" body="创建一条链接，放入一行或多行限时内容。" action={<Link className="button secondary" to="/shares/new">创建第一条分享</Link>} />
          ) : (
            <div className="share-list">
              {shares.map((share) => {
                const expired = share.expiresAt <= Date.now() / 1000 || Boolean(share.revokedAt);
                return (
                  <article className={`share-card ${expired ? "is-expired" : ""}`} key={share.id}>
                    <div className="share-main">
                      <div className="share-title-row">
                        <span className={`status ${expired ? "off" : share.availableCount ? "live" : "done"}`}>
                          {expired ? "已失效" : share.availableCount ? "进行中" : "已领完"}
                        </span>
                        <h2>{share.title}</h2>
                      </div>
                      <div className="share-meta">
                        {user.role === "admin" && <span>创建者 {share.ownerName}</span>}
                        <span>{share.availableCount} / {share.itemCount} 份可领取</span>
                        <span>链接到期 {formatDate(share.expiresAt)}</span>
                        <span>单会话 {formatDuration(share.sessionTimeoutSeconds)}</span>
                      </div>
                    </div>
                    <div className="share-actions">
                      <button className="button secondary small" disabled={expired} onClick={() => void copyLink(`share:${share.id}`, share.publicLink)}>
                        {copied === `share:${share.id}` ? "已复制" : "复制链接"}
                      </button>
                      <button className="button danger-ghost small" onClick={() => void destroyShare(share)}>销毁</button>
                    </div>
                  </article>
                );
              })}
            </div>
          )}

          <h2 className="list-heading">直接订阅</h2>
          {subscriptions.length === 0 ? (
            <EmptyState title="还没有订阅" body="导入或粘贴 Clash YAML、Base64 或其它配置文本，生成无需领取的直出链接。" action={<Link className="button secondary" to="/subscriptions/new">创建第一条订阅</Link>} />
          ) : (
            <div className="share-list">
              {subscriptions.map((subscription) => {
                const expired = subscription.expiresAt > 0 && subscription.expiresAt <= Date.now() / 1000;
                const exhausted = subscription.maxRequests > 0 && subscription.successfulRequests >= subscription.maxRequests;
                const active = !expired && !exhausted;
                return (
                  <article className={`share-card subscription-card ${active ? "" : "is-expired"}`} key={subscription.id}>
                    <div className="share-main">
                      <div className="share-title-row">
                        <span className={`status ${active ? "live" : "off"}`}>{active ? "可订阅" : expired ? "已过期" : "次数用完"}</span>
                        <h2>{subscription.title}</h2>
                      </div>
                      <div className="share-meta">
                        {user.role === "admin" && <span>创建者 {subscription.ownerName}</span>}
                        <span>{subscription.maxRequests === 0 ? "剩余无限" : `剩余 ${Math.max(0, subscription.maxRequests - subscription.successfulRequests)} 次`} · 已成功 {subscription.successfulRequests} 次</span>
                        <span>{subscription.expiresAt === 0 ? "永不过期" : `到期 ${formatDate(subscription.expiresAt)}`}</span>
                        <span>{subscription.contentType}{subscription.base64Encoded ? " · Base64" : ""}</span>
                      </div>
                    </div>
                    <div className="share-actions">
                      <button className="button secondary small" disabled={!active} onClick={() => void copyLink(`subscription:${subscription.id}`, subscription.publicLink)}>
                        {copied === `subscription:${subscription.id}` ? "已复制" : "复制链接"}
                      </button>
                      <button className="button secondary small" disabled={editLoadingId === subscription.id} onClick={() => void toggleEdit(subscription)}>
                        {editLoadingId === subscription.id ? "读取中…" : editState?.id === subscription.id ? "收起修改" : "修改"}
                      </button>
                      <button className="button danger-ghost small" onClick={() => void destroySubscription(subscription)}>删除</button>
                    </div>
                    {editState?.id === subscription.id && (
                      <form className="subscription-edit-panel" onSubmit={(event) => void saveSubscription(event)}>
                        <div className="edit-summary">
                          <div><strong>{editState.active ? "修改订阅" : "修改并重新启用"}</strong><p>保存后原链接立即使用新内容；累计成功次数不会重置。</p></div>
                          <span>已成功 {editState.successfulRequests} 次</span>
                        </div>
                        <label>订阅内容
                          <textarea className="subscription-editor" rows={18} value={editState.content} onChange={(e) => setEditState({ ...editState, content: e.target.value })} required />
                          <small>{formatBytes(new TextEncoder().encode(editState.content).length)} / {formatBytes(config?.maxTextBytes || 0)} · {editState.contentType}{editState.base64Encoded ? " · Base64" : ""}</small>
                        </label>
                        <div className="form-grid two">
                          <label>剩余请求次数<input type="number" min={1} step={1} value={editState.remainingRequests} onChange={(e) => setEditState({ ...editState, remainingRequests: Number(e.target.value) })} disabled={editState.unlimitedRequests} required={!editState.unlimitedRequests} /></label>
                          <label className="toggle-row compact"><input type="checkbox" checked={editState.unlimitedRequests} onChange={(e) => setEditState({ ...editState, unlimitedRequests: e.target.checked })} /><span><strong>无限请求次数</strong><small>累计成功次数仍会继续统计。</small></span></label>
                          <label>保存后有效期（小时）<input type="number" min={1 / 60} max={(config?.maxLinkValiditySeconds || 86400) / 3600} step={1 / 60} value={editState.validityHours} onChange={(e) => setEditState({ ...editState, validityHours: Number(e.target.value) })} disabled={editState.neverExpires} required={!editState.neverExpires} /></label>
                          <label className="toggle-row compact"><input type="checkbox" checked={editState.neverExpires} onChange={(e) => setEditState({ ...editState, neverExpires: e.target.checked })} /><span><strong>永不过期</strong><small>保留相同订阅链接。</small></span></label>
                        </div>
                        <div className="button-row">
                          <button className="button primary small" type="submit" disabled={editSaving}>{editSaving ? "正在保存…" : editState.active ? "保存修改" : "保存并重新启用"}</button>
                          <button className="button ghost small" type="button" onClick={() => setEditState(null)} disabled={editSaving}>取消</button>
                        </div>
                      </form>
                    )}
                  </article>
                );
              })}
            </div>
          )}
        </>
      )}
    </Layout>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return <div className="metric"><strong>{value}</strong><span>{label}</span></div>;
}

function formatDuration(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`;
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

function formatDate(unix: number): string {
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(new Date(unix * 1000));
}
