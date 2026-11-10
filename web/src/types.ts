export type Role = "admin" | "user";

export interface User {
  id: string;
  username: string;
  role: Role;
  disabled: boolean;
  createdAt: number;
}

export interface Settings {
  maxFileBytes: number;
  maxTotalStorageBytes: number;
  maxActiveSharesPerUser: number;
  maxItemsPerShare: number;
  maxSessionTimeoutSeconds: number;
  maxLinkValiditySeconds: number;
  defaultSessionTimeoutSeconds: number;
  defaultLinkValiditySeconds: number;
  maxTextBytes: number;
  maxColumns: number;
  allowedExtensions: string[];
  mimeMap: Record<string, string>;
  turnstileEnabled: boolean;
  turnstileSiteKey: string;
  turnstileSecretConfigured: boolean;
  turnstileSecretKey?: string;
  turnstileClearSecret?: boolean;
}

export interface TurnstileConfig {
  enabled: boolean;
  siteKey: string;
  action: string;
  shareId: string;
}

export interface UserConfig {
  maxFileBytes: number;
  maxItemsPerShare: number;
  maxSessionTimeoutSeconds: number;
  maxLinkValiditySeconds: number;
  defaultSessionTimeoutSeconds: number;
  defaultLinkValiditySeconds: number;
  maxTextBytes: number;
  maxColumns: number;
  allowedExtensions: string[];
}

export interface Block {
  type: "markdown" | "image" | "file";
  text?: string;
  align?: "left" | "center" | "right";
  assetId?: string;
  assetName?: string;
  mimeType?: string;
  size?: number;
}

export interface ItemInput {
  blocks: Block[];
}

export interface Share {
  id: string;
  ownerId: string;
  ownerName?: string;
  publicId: string;
  title: string;
  sessionTimeoutSeconds: number;
  expiresAt: number;
  ipAllow: string[];
  ipDeny: string[];
  defaultAlign: "left" | "center" | "right";
  createdAt: number;
  revokedAt?: number;
  publicLink?: string;
  itemCount: number;
  availableCount: number;
  consumedCount: number;
}

export interface Asset {
  id: string;
  name: string;
  mimeType: string;
  size: number;
  extension: string;
  createdAt: number;
  itemId?: string;
}

export interface ClaimItem {
  blocks: Block[];
}

export interface ClaimContent {
  claimId: string;
  shareTitle: string;
  defaultAlign: "left" | "center" | "right";
  expiresAt: number;
  leaseExpiresAt: number;
  items: ClaimItem[];
}

