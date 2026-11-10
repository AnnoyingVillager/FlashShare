import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api";
import { Layout, PageHeading } from "../components/Layout";
import type { User } from "../types";

export function AdminUsersPage({ user, onLogout }: { user: User; onLogout: () => void }) {
  const [users, setUsers] = useState<User[]>([]);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"user" | "admin">("user");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const result = await api.users();
      setUsers(result.users);
    } catch (err) {
      setError(errorMessage(err));
    }
  }
  useEffect(() => { void load(); }, []);

  async function create(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.createUser(username, password, role);
      setUsername("");
      setPassword("");
      setRole("user");
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function toggle(target: User) {
    try {
      await api.updateUser(target.id, { disabled: !target.disabled });
      await load();
    } catch (err) { setError(errorMessage(err)); }
  }

  async function resetPassword(target: User) {
    const next = window.prompt(`为 ${target.username} 设置新密码（至少 10 个字符）`);
    if (!next) return;
    try {
      await api.updateUser(target.id, { password: next });
      window.alert("密码已更新，该用户现有登录会话已失效。");
    } catch (err) { setError(errorMessage(err)); }
  }

  async function remove(target: User) {
    if (!window.confirm(`删除用户 ${target.username}，并立即销毁其所有分享？`)) return;
    try {
      await api.deleteUser(target.id);
      await load();
    } catch (err) { setError(errorMessage(err)); }
  }

  return (
    <Layout user={user} onLogout={onLogout} wide>
      <PageHeading eyebrow="ACCESS CONTROL" title="用户管理" description="普通用户不会自行注册。禁用或删除账号时，对应的后台登录会话会立即失效。" />
      {error && <div className="alert error">{error}</div>}
      <div className="admin-grid">
        <section className="panel">
          <h2>手动创建账号</h2>
          <form className="stack-form" onSubmit={create}>
            <label>用户名<input value={username} onChange={(e) => setUsername(e.target.value)} required minLength={3} maxLength={64} placeholder="例如 alice" /></label>
            <label>初始密码<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={10} /></label>
            <label>角色<select value={role} onChange={(e) => setRole(e.target.value as "user" | "admin")}><option value="user">普通用户</option><option value="admin">管理员</option></select></label>
            <button className="button primary" disabled={busy}>{busy ? "创建中…" : "创建账号"}</button>
          </form>
        </section>
        <section className="panel user-list-panel">
          <div className="panel-title"><h2>现有账号</h2><span>{users.length} 个</span></div>
          <div className="user-list">
            {users.map((item) => (
              <article className={`user-row ${item.disabled ? "disabled" : ""}`} key={item.id}>
                <div className="avatar">{item.username.slice(0, 2).toUpperCase()}</div>
                <div className="user-identity"><strong>{item.username}</strong><span>{item.role === "admin" ? "管理员" : "普通用户"}{item.disabled ? " · 已禁用" : ""}</span></div>
                <div className="button-row">
                  <button className="button ghost small" onClick={() => void resetPassword(item)}>改密码</button>
                  <button className="button ghost small" disabled={item.id === user.id} onClick={() => void toggle(item)}>{item.disabled ? "启用" : "禁用"}</button>
                  <button className="button danger-ghost small" disabled={item.id === user.id} onClick={() => void remove(item)}>删除</button>
                </div>
              </article>
            ))}
          </div>
        </section>
      </div>
    </Layout>
  );
}

