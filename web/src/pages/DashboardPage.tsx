import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, errorMessage } from "../api";
import { EmptyState, Layout, PageHeading } from "../components/Layout";
import type { Share, User } from "../types";

export function DashboardPage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [shares, setShares] = useState<Share[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");

  async function load() {
    setLoading(true);
    try {
      const result = await api.shares();
      setShares(result.shares);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  async function copy(share: Share) {
    if (!share.publicLink) return;
    await navigator.clipboard.writeText(share.publicLink);
    setCopied(share.id);
    window.setTimeout(() => setCopied(""), 1800);
  }

  async function destroy(share: Share) {
    if (!window.confirm(`立即销毁“${share.title}”的所有未领取和已领取内容？`)) return;
    try {
      await api.deleteShare(share.id);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  return (
    <Layout user={user} onLogout={onLogout} wide>
      <PageHeading
        eyebrow="WELCOME BACK"
        title="分享控制台"
        description="每条链接按标签页会话分配一份内容；内容到期或页面关闭后从服务端销毁。"
        actions={<Link className="button primary" to="/shares/new">＋ 新建分享</Link>}
      />
      <div className="metric-row">
        <Metric label="分享链接" value={shares.length} />
        <Metric label="可领取内容" value={shares.reduce((sum, s) => sum + s.availableCount, 0)} />
        <Metric label="已消耗内容" value={shares.reduce((sum, s) => sum + s.consumedCount, 0)} />
      </div>
      {error && <div className="alert error">{error}</div>}
      {loading ? <div className="panel loading-panel">正在读取分享…</div> : shares.length === 0 ? (
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
                  <button className="button secondary small" disabled={expired} onClick={() => void copy(share)}>
                    {copied === share.id ? "已复制" : "复制链接"}
                  </button>
                  <button className="button danger-ghost small" onClick={() => void destroy(share)}>销毁</button>
                </div>
              </article>
            );
          })}
        </div>
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

