import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { EmptyState, Layout, PageHeading } from "../components/Layout";
import type { Share, Subscription, User, UserConfig } from "../types";

export function DashboardPage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [shares, setShares] = useState<Share[]>([]);
  const [subscriptions, setSubscriptions] = useState<Subscription[]>([]);
  const [config, setConfig] = useState<UserConfig | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const [reactivatingId, setReactivatingId] = useState("");
  const [reactivationNeverExpires, setReactivationNeverExpires] = useState(false);
  const [reactivationValidityHours, setReactivationValidityHours] = useState(24);
  const [reactivationUnlimitedRequests, setReactivationUnlimitedRequests] = useState(false);
  const [reactivationMaxRequests, setReactivationMaxRequests] = useState(100);

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
      await load();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  function startReactivation(subscription: Subscription) {
    setReactivatingId(subscription.id);
    setReactivationNeverExpires(subscription.expiresAt === 0);
    setReactivationValidityHours(Math.max(1 / 60, (config?.defaultLinkValiditySeconds || 86400) / 3600));
    setReactivationUnlimitedRequests(subscription.maxRequests === 0);
    setReactivationMaxRequests(subscription.maxRequests > 0 ? subscription.maxRequests : 100);
    setError("");
  }

  async function reactivate(event: FormEvent) {
    event.preventDefault();
    if (!reactivatingId || !config) return;
    const validitySeconds = reactivationNeverExpires ? 0 : Math.round(reactivationValidityHours * 3600);
    if (!reactivationNeverExpires && (validitySeconds < 60 || validitySeconds > config.maxLinkValiditySeconds)) {
      setError(`有效期必须在 1 分钟到 ${Math.floor(config.maxLinkValiditySeconds / 3600)} 小时之间`);
      return;
    }
    if (!reactivationUnlimitedRequests && (!Number.isInteger(reactivationMaxRequests) || reactivationMaxRequests < 1)) {
      setError("请求上限必须是大于 0 的整数");
      return;
    }
    try {
      await api.reactivateSubscription(reactivatingId, {
        linkValiditySeconds: validitySeconds,
        maxRequests: reactivationUnlimitedRequests ? 0 : reactivationMaxRequests,
      });
      setReactivatingId("");
      await load();
    } catch (err) {
      setError(errorMessage(err));
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
                        <span>{subscription.successfulRequests} / {subscription.maxRequests === 0 ? "∞" : subscription.maxRequests} 次成功请求</span>
                        <span>{subscription.expiresAt === 0 ? "永不过期" : `到期 ${formatDate(subscription.expiresAt)}`}</span>
                        <span>{subscription.contentType}{subscription.base64Encoded ? " · Base64" : ""}</span>
                      </div>
                    </div>
                    <div className="share-actions">
                      <button className="button secondary small" disabled={!active} onClick={() => void copyLink(`subscription:${subscription.id}`, subscription.publicLink)}>
                        {copied === `subscription:${subscription.id}` ? "已复制" : "复制链接"}
                      </button>
                      {!active && <button className="button secondary small" onClick={() => startReactivation(subscription)}>重新启用</button>}
                      <button className="button danger-ghost small" onClick={() => void destroySubscription(subscription)}>删除</button>
                    </div>
                    {reactivatingId === subscription.id && (
                      <form className="reactivation-panel" onSubmit={(event) => void reactivate(event)}>
                        <div className="form-grid two">
                          <label>新请求上限<input type="number" min={1} step={1} value={reactivationMaxRequests} onChange={(e) => setReactivationMaxRequests(Number(e.target.value))} disabled={reactivationUnlimitedRequests} required={!reactivationUnlimitedRequests} /></label>
                          <label className="toggle-row compact"><input type="checkbox" checked={reactivationUnlimitedRequests} onChange={(e) => setReactivationUnlimitedRequests(e.target.checked)} /><span><strong>无限请求次数</strong><small>重新启用后从 0 开始统计。</small></span></label>
                          <label>新有效期（小时）<input type="number" min={1 / 60} max={(config?.maxLinkValiditySeconds || 86400) / 3600} step={1 / 60} value={reactivationValidityHours} onChange={(e) => setReactivationValidityHours(Number(e.target.value))} disabled={reactivationNeverExpires} required={!reactivationNeverExpires} /></label>
                          <label className="toggle-row compact"><input type="checkbox" checked={reactivationNeverExpires} onChange={(e) => setReactivationNeverExpires(e.target.checked)} /><span><strong>永不过期</strong><small>保留相同链接和内容。</small></span></label>
                        </div>
                        <div className="button-row">
                          <button className="button primary small" type="submit">确认重新启用</button>
                          <button className="button ghost small" type="button" onClick={() => setReactivatingId("")}>取消</button>
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
