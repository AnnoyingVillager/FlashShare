import type { Asset, Block, ClaimContent, ItemInput, Settings, Share, TurnstileConfig, User, UserConfig } from "./types";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  if (options.body && !(options.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const response = await fetch(path, { ...options, headers, credentials: "same-origin" });
  const contentType = response.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await response.json() : null;
  if (!response.ok) {
    throw new ApiError(response.status, body?.error || `请求失败 (${response.status})`);
  }
  return body as T;
}

export const api = {
  setupStatus: () => request<{ needsSetup: boolean }>("/api/setup/status"),
  setup: (username: string, password: string) =>
    request<{ user: User }>("/api/setup", { method: "POST", body: JSON.stringify({ username, password }) }),
  login: (username: string, password: string) =>
    request<{ user: User }>("/api/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  logout: () => request<{ ok: boolean }>("/api/auth/logout", { method: "POST" }),
  me: () => request<{ user: User }>("/api/me"),
  config: () => request<{ config: UserConfig }>("/api/config"),

  users: () => request<{ users: User[] }>("/api/users"),
  createUser: (username: string, password: string, role: "user" | "admin") =>
    request<{ user: User }>("/api/users", { method: "POST", body: JSON.stringify({ username, password, role }) }),
  updateUser: (id: string, patch: { disabled?: boolean; password?: string; role?: "user" | "admin" }) =>
    request<{ ok: boolean }>(`/api/users/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),
  deleteUser: (id: string) => request<{ ok: boolean }>(`/api/users/${id}`, { method: "DELETE" }),

  settings: () => request<{ settings: Settings }>("/api/admin/settings"),
  saveSettings: (settings: Settings) =>
    request<{ settings: Settings }>("/api/admin/settings", { method: "PUT", body: JSON.stringify(settings) }),

  shares: () => request<{ shares: Share[] }>("/api/shares"),
  createShare: (payload: {
    title: string;
    sessionTimeoutSeconds: number;
    linkValiditySeconds: number;
    ipAllow: string[];
    ipDeny: string[];
    defaultAlign: "left" | "center" | "right";
    items: ItemInput[];
  }) => request<{ share: Share }>("/api/shares", { method: "POST", body: JSON.stringify(payload) }),
  deleteShare: (id: string) => request<{ ok: boolean }>(`/api/shares/${id}`, { method: "DELETE" }),

  uploadAsset: async (file: File): Promise<Asset> => {
    const data = new FormData();
    data.append("file", file);
    const result = await request<{ asset: Asset }>("/api/assets", { method: "POST", body: data });
    return result.asset;
  },

  turnstileConfig: (publicId: string) =>
    request<TurnstileConfig>(`/api/public/shares/${publicId}/turnstile`),
  claim: (publicId: string, secret: string, turnstileToken = "") =>
    request<{ claimId: string; claimToken: string; shareTitle: string; defaultAlign: string }>(
      `/api/public/shares/${publicId}/claim`,
      { method: "POST", body: JSON.stringify({ secret, turnstileToken }) }
    ),
  claimContent: (claimId: string, token: string) =>
    request<ClaimContent>(`/api/public/claims/${claimId}`, { headers: { Authorization: `Bearer ${token}` } }),
  startClaim: (claimId: string, token: string) =>
    request<{ expiresAt: number; leaseExpiresAt: number }>(`/api/public/claims/${claimId}/start`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` }
    }),
  claimAsset: (claimId: string, token: string, assetId: string) =>
    fetch(`/api/public/claims/${claimId}/assets/${assetId}`, {
      headers: { Authorization: `Bearer ${token}` },
      credentials: "same-origin"
    }),
  ownerAsset: (assetId: string) => `/api/assets/${assetId}/preview`
};

export function assetNote(block: Block): string {
  return formatBytes(block.size || 0);
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GiB`;
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "发生未知错误";
}

