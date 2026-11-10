import { useEffect, useMemo, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useParams } from "react-router-dom";
import { api, errorMessage, formatBytes, ApiError } from "../api";
import { LoadingScreen } from "../components/LoadingScreen";
import { TurnstileWidget } from "../components/TurnstileWidget";
import type { Block, ClaimContent, TurnstileConfig } from "../types";

type SavedClaim = { claimId: string; token: string };
type PageState = "checking" | "claimable" | "loading" | "viewing" | "expired" | "error";

export function PublicSharePage() {
  const { publicId = "" } = useParams();
  const secret = useMemo(() => new URLSearchParams(window.location.hash.slice(1)).get("k") || "", [publicId]);
  const storageKey = `flashshare:claim:${publicId}`;
  const [state, setState] = useState<PageState>("checking");
  const [content, setContent] = useState<ClaimContent | null>(null);
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  const [claimBusy, setClaimBusy] = useState(false);
  const [turnstile, setTurnstile] = useState<TurnstileConfig | null>(null);
  const [turnstileToken, setTurnstileToken] = useState("");
  const [turnstileError, setTurnstileError] = useState("");
  const [turnstileReset, setTurnstileReset] = useState(0);
  const initialized = useRef(false);
  const socketRef = useRef<WebSocket | null>(null);
  const unmounted = useRef(false);

  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    unmounted.current = false;
    if (!secret) {
      setState("error");
      setError("链接缺少领取密钥，请向分享者获取完整链接。");
      return;
    }
    const saved = readSavedClaim(storageKey);
    if (saved) {
      void loadClaim(saved);
    } else {
      void loadSecurityConfig();
    }
    return () => {
      unmounted.current = true;
      socketRef.current?.close();
    };
  }, []);

  useEffect(() => {
    if (state !== "viewing") return;
    const timer = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [state]);

  useEffect(() => {
    if (state === "viewing" && content && content.expiresAt > 0 && now >= content.expiresAt * 1000) {
      expireLocally();
    }
  }, [now, content, state]);

  function saveClaim(value: SavedClaim) {
    sessionStorage.setItem(storageKey, JSON.stringify(value));
  }

  async function loadSecurityConfig() {
    try {
      const config = await api.turnstileConfig(publicId);
      if (unmounted.current) return;
      setTurnstile(config);
      setState("claimable");
    } catch (err) {
      if (err instanceof ApiError && (err.status === 404 || err.status === 410)) {
        setState("expired");
      } else {
        setState("error");
        setError(errorMessage(err));
      }
    }
  }

  async function loadClaim(saved: SavedClaim) {
    setState("loading");
    setError("");
    try {
      const [loaded, started] = await Promise.all([
        api.claimContent(saved.claimId, saved.token),
        api.startClaim(saved.claimId, saved.token)
      ]);
      if (unmounted.current) return;
      const next = { ...loaded, expiresAt: started.expiresAt, leaseExpiresAt: started.leaseExpiresAt };
      setContent(next);
      setNow(Date.now());
      setState("viewing");
      connectSocket(saved);
    } catch (err) {
      if (err instanceof ApiError && (err.status === 404 || err.status === 410)) {
        sessionStorage.removeItem(storageKey);
        setState("expired");
      } else {
        setState("error");
        setError(errorMessage(err));
      }
    }
  }

  async function claim() {
    setClaimBusy(true);
    setError("");
    try {
      const result = await api.claim(publicId, secret, turnstileToken);
      const saved = { claimId: result.claimId, token: result.claimToken };
      saveClaim(saved);
      await loadClaim(saved);
    } catch (err) {
      if (err instanceof ApiError && (err.status === 410 || err.status === 404)) {
        setState("expired");
      } else {
        setError(errorMessage(err));
        setState("claimable");
        if (turnstile?.enabled) {
          setTurnstileToken("");
          setTurnstileReset((value) => value + 1);
        }
      }
    } finally {
      setClaimBusy(false);
    }
  }

  function connectSocket(saved: SavedClaim) {
    socketRef.current?.close();
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(`${protocol}//${window.location.host}/api/public/claims/${saved.claimId}/ws`, ["flashshare.v1", saved.token]);
    socketRef.current = socket;
    socket.onclose = (event) => {
      if (unmounted.current || socketRef.current !== socket) return;
      if (event.code === 4001) {
        expireLocally();
        return;
      }
      if (readSavedClaim(storageKey)) {
        window.setTimeout(() => {
          if (!unmounted.current && socketRef.current === socket && readSavedClaim(storageKey)) connectSocket(saved);
        }, 1200);
      }
    };
  }

  function expireLocally() {
    socketRef.current?.close();
    socketRef.current = null;
    sessionStorage.removeItem(storageKey);
    setContent(null);
    setState("expired");
  }

  if (state === "checking" || state === "loading") return <LoadingScreen label="正在恢复限时会话" />;

  if (state === "expired") {
    return (
      <BurnPage code="EXPIRED" title="内容已销毁">
        此领取会话已经结束，服务端不再保存对应内容。如需下一份内容，请由分享者重新创建链接。
      </BurnPage>
    );
  }
  if (state === "error") {
    return <BurnPage code="UNAVAILABLE" title="无法打开分享">{error}</BurnPage>;
  }
  if (state === "claimable") {
    return (
      <div className="public-page">
        <div className="public-brand"><span className="brand-spark">F</span> FlashShare</div>
        <section className="claim-card">
          <div className="locked-orb">◇</div>
          <div className="eyebrow">ONE SESSION DELIVERY</div>
          <h1>领取此链接中的一份内容</h1>
          <p>内容会绑定到当前标签页。刷新不会重新分配，关闭页面后会在短暂宽限期后销毁。</p>
          {turnstile?.enabled && (
            <div className="turnstile-panel">
              <div className="turnstile-label"><span>Cloudflare 安全校验</span><small>完成验证后才能领取内容</small></div>
              <TurnstileWidget
                key={turnstileReset}
                siteKey={turnstile.siteKey}
                publicId={publicId}
                resetSignal={turnstileReset}
                onToken={(token) => { setTurnstileToken(token); if (token) setTurnstileError(""); }}
                onError={setTurnstileError}
              />
            </div>
          )}
          {turnstileError && <div className="alert error">{turnstileError}</div>}
          {error && <div className="alert error">{error}</div>}
          <button className="button primary large full" disabled={claimBusy || Boolean(turnstile?.enabled && !turnstileToken)} onClick={() => void claim()}>{claimBusy ? "正在分配内容…" : turnstile?.enabled && !turnstileToken ? "请先完成人机验证" : "领取并显示内容"}</button>
          <ul className="claim-rules"><li>不要通过公开渠道转发链接</li><li>内容仍可被截图、复制或下载</li><li>服务器时钟是最终截止时间</li></ul>
        </section>
      </div>
    );
  }

  if (!content) return <LoadingScreen label="正在载入内容" />;
  const secondsLeft = Math.max(0, Math.ceil((content.expiresAt * 1000 - now) / 1000));
  return (
    <div className="public-page content-page">
      <header className="public-header">
        <div className="public-brand"><span className="brand-spark">F</span> FlashShare</div>
        <div className={`countdown ${secondsLeft < 30 ? "urgent" : ""}`}>
          <span>本会话剩余</span><strong>{formatCountdown(secondsLeft)}</strong>
        </div>
      </header>
      <main className="claim-content">
        <div className="reveal-banner"><span className="live-dot" /> 内容仅在此标签页会话中有效</div>
        <h1>{content.shareTitle}</h1>
        {content.items.map((item, itemIndex) => (
          <article className="content-package" key={itemIndex}>
            {item.blocks.map((block, blockIndex) => (
              <ClaimBlock key={blockIndex} block={block} claimId={content.claimId} token={readSavedClaim(storageKey)?.token || ""} onGone={expireLocally} />
            ))}
          </article>
        ))}
        <div className="burn-notice"><strong>请立即使用</strong><span>倒计时结束后页面内容会被清空，服务端同时销毁数据。</span></div>
      </main>
    </div>
  );
}

function ClaimBlock({ block, claimId, token, onGone }: { block: Block; claimId: string; token: string; onGone: () => void }) {
  if (block.type === "markdown") {
    return (
      <div className={`markdown-block align-${block.align || "center"}`}>
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          skipHtml
          components={{
            img: () => null,
            a: (props) => <a {...props} target="_blank" rel="noopener noreferrer nofollow" />
          }}
        >{block.text || ""}</ReactMarkdown>
      </div>
    );
  }
  if (!block.assetId) return null;
  return <AssetBlock block={block} claimId={claimId} token={token} onGone={onGone} />;
}

function AssetBlock({ block, claimId, token, onGone }: { block: Block; claimId: string; token: string; onGone: () => void }) {
  const [imageUrl, setImageUrl] = useState("");
  const [error, setError] = useState("");
  const [downloading, setDownloading] = useState(false);

  useEffect(() => {
    if (block.type !== "image" || !block.assetId) return;
    let url = "";
    let active = true;
    api.claimAsset(claimId, token, block.assetId).then(async (response) => {
      if (!response.ok) throw new Error(response.status === 410 ? "附件已销毁" : "无法读取图片");
      const blob = await response.blob();
      if (!active) return;
      url = URL.createObjectURL(blob);
      setImageUrl(url);
    }).catch((err) => { if (active) setError(errorMessage(err)); });
    return () => { active = false; if (url) URL.revokeObjectURL(url); };
  }, [block.assetId, block.type, claimId, token]);

  async function download() {
    if (!block.assetId) return;
    setDownloading(true);
    setError("");
    try {
      const response = await api.claimAsset(claimId, token, block.assetId);
      if (response.status === 410) { onGone(); return; }
      if (!response.ok) throw new Error("附件已无法读取");
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = block.assetName || "download";
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) { setError(errorMessage(err)); }
    finally { setDownloading(false); }
  }

  if (block.type === "image") {
    return (
      <figure className="image-block">
        {imageUrl ? <img src={imageUrl} alt={block.assetName || "分享图片"} /> : !error && <div className="image-loading">正在解密图片…</div>}
        {error && <div className="alert error">{error}</div>}
        <figcaption>{block.assetName}</figcaption>
      </figure>
    );
  }
  return (
    <div className="file-block">
      <div className="file-glyph">↧</div>
      <div><strong>{block.assetName}</strong><span>{formatBytes(block.size || 0)} · 下载后不再受本页销毁控制</span></div>
      <button className="button secondary" disabled={downloading} onClick={() => void download()}>{downloading ? "读取中…" : "解密并下载"}</button>
      {error && <div className="alert error">{error}</div>}
    </div>
  );
}

function BurnPage({ code, title, children }: { code: string; title: string; children: React.ReactNode }) {
  return (
    <div className="public-page">
      <div className="public-brand"><span className="brand-spark">F</span> FlashShare</div>
      <section className="burn-card"><div className="burn-ring" /><div className="eyebrow">{code}</div><h1>{title}</h1><p>{children}</p><span className="fine-print">没有任何可恢复的内容链接。</span></section>
    </div>
  );
}

function readSavedClaim(key: string): SavedClaim | null {
  try {
    const raw = sessionStorage.getItem(key);
    if (!raw) return null;
    const value = JSON.parse(raw) as SavedClaim;
    return value.claimId && value.token ? value : null;
  } catch {
    return null;
  }
}

function formatCountdown(seconds: number): string {
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(rest).padStart(2, "0")}`;
}

