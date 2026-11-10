import type { ReactNode } from "react";
import { NavLink, Link } from "react-router-dom";
import type { User } from "../types";

export function Layout({ user, onLogout, children, wide = false }: {
  user: User;
  onLogout: () => void;
  children: ReactNode;
  wide?: boolean;
}) {
  return (
    <div className="app-shell">
      <header className="topbar">
        <Link className="brand" to="/">
          <span className="brand-spark">F</span>
          <span>FlashShare</span>
        </Link>
        <nav className="main-nav" aria-label="主导航">
          <NavLink to="/" end>分享</NavLink>
          <NavLink to="/shares/new">新建</NavLink>
          {user.role === "admin" && <NavLink to="/admin/users">用户</NavLink>}
          {user.role === "admin" && <NavLink to="/admin/settings">设置</NavLink>}
        </nav>
        <div className="account">
          <span className="role-dot" title={user.role === "admin" ? "管理员" : "普通用户"} />
          <span>{user.username}</span>
          <button className="button ghost small" onClick={onLogout}>退出</button>
        </div>
      </header>
      <main className={wide ? "page page-wide" : "page"}>{children}</main>
    </div>
  );
}

export function PageHeading({ eyebrow, title, description, actions }: {
  eyebrow?: string;
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {actions && <div className="heading-actions">{actions}</div>}
    </div>
  );
}

export function EmptyState({ title, body, action }: { title: string; body: string; action?: ReactNode }) {
  return (
    <div className="empty-state">
      <div className="empty-icon">↗</div>
      <h3>{title}</h3>
      <p>{body}</p>
      {action}
    </div>
  );
}

