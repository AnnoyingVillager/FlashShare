import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api";
import type { User } from "../types";

export function AuthPage({ mode, onAuthenticated }: { mode: "setup" | "login"; onAuthenticated: (user: User) => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (mode === "setup" && password !== confirm) {
      setError("两次输入的密码不一致");
      return;
    }
    setBusy(true);
    try {
      const result = mode === "setup" ? await api.setup(username, password) : await api.login(username, password);
      onAuthenticated(result.user);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-page">
      <section className="auth-story">
        <div className="brand auth-brand"><span className="brand-spark">F</span><span>FlashShare</span></div>
        <div className="auth-copy">
          <div className="eyebrow">SESSION-BOUND DELIVERY</div>
          <h1>一次性分享，<br />只停留在此刻。</h1>
          <p>把兑换码、临时账号或附件绑定到一个短暂页面会话。服务端到期即销毁，刷新不延长生命周期。</p>
        </div>
        <div className="linear-note">
          <span>01 创建</span><i /><span>02 领取</span><i /><span>03 销毁</span>
        </div>
      </section>
      <section className="auth-panel">
        <form className="auth-card" onSubmit={submit}>
          <div className="eyebrow">{mode === "setup" ? "FIRST RUN" : "CONTROL ROOM"}</div>
          <h2>{mode === "setup" ? "创建首位管理员" : "登录管理台"}</h2>
          <p className="muted">{mode === "setup" ? "系统仅允许管理员手动创建后续普通用户。" : "账号仅对本站管理员和获授权的普通用户开放。"}</p>
          <label>用户名<input autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required minLength={3} /></label>
          <label>密码<input type="password" autoComplete={mode === "setup" ? "new-password" : "current-password"} value={password} onChange={(e) => setPassword(e.target.value)} required minLength={10} /></label>
          {mode === "setup" && <label>确认密码<input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required minLength={10} /></label>}
          {error && <div className="alert error">{error}</div>}
          <button className="button primary full" disabled={busy}>{busy ? "请稍候…" : mode === "setup" ? "初始化并进入" : "登录"}</button>
        </form>
      </section>
    </div>
  );
}

