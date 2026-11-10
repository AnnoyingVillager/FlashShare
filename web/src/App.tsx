import { useEffect, useState } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { api } from "./api";
import { AuthPage } from "./pages/AuthPage";
import { DashboardPage } from "./pages/DashboardPage";
import { CreateSharePage } from "./pages/CreateSharePage";
import { AdminUsersPage } from "./pages/AdminUsersPage";
import { AdminSettingsPage } from "./pages/AdminSettingsPage";
import { PublicSharePage } from "./pages/PublicSharePage";
import type { User } from "./types";
import { LoadingScreen } from "./components/LoadingScreen";

export default function App() {
  return (
    <Routes>
      <Route path="/s/:publicId" element={<PublicSharePage />} />
      <Route path="/*" element={<ProtectedApp />} />
    </Routes>
  );
}

function ProtectedApp() {
  const [state, setState] = useState<"loading" | "setup" | "login" | "ready">("loading");
  const [user, setUser] = useState<User | null>(null);

  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const setup = await api.setupStatus();
        if (!active) return;
        if (setup.needsSetup) {
          setState("setup");
          return;
        }
        try {
          const result = await api.me();
          if (!active) return;
          setUser(result.user);
          setState("ready");
        } catch {
          if (active) setState("login");
        }
      } catch {
        if (active) setState("login");
      }
    }
    void load();
    return () => { active = false; };
  }, []);

  if (state === "loading") return <LoadingScreen label="正在连接 FlashShare" />;
  if (state === "setup" || state === "login") {
    return <AuthPage mode={state} onAuthenticated={(value) => { setUser(value); setState("ready"); }} />;
  }
  if (!user) return <LoadingScreen label="正在读取账号" />;

  async function logout() {
    await api.logout().catch(() => undefined);
    setUser(null);
    setState("login");
  }

  return (
    <Routes>
      <Route path="/" element={<DashboardPage user={user} onLogout={logout} />} />
      <Route path="/shares/new" element={<CreateSharePage user={user} onLogout={logout} />} />
      <Route path="/admin/users" element={user.role === "admin" ? <AdminUsersPage user={user} onLogout={logout} /> : <Navigate to="/" />} />
      <Route path="/admin/settings" element={user.role === "admin" ? <AdminSettingsPage user={user} onLogout={logout} /> : <Navigate to="/" />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

