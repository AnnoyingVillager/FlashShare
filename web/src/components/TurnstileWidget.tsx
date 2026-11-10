import { useEffect, useRef } from "react";

interface TurnstileRenderOptions {
  sitekey: string;
  theme: "auto" | "light" | "dark";
  language: string;
  action: string;
  cdata: string;
  size: "normal" | "flexible" | "compact";
  callback: (token: string) => void;
  "expired-callback": () => void;
  "timeout-callback": () => void;
  "error-callback": (errorCode: string) => void;
}

interface TurnstileApi {
  render: (container: HTMLElement, options: TurnstileRenderOptions) => string;
  remove: (widgetId: string) => void;
  reset: (widgetId?: string) => void;
}

declare global {
  interface Window {
    turnstile?: TurnstileApi;
  }
}

let scriptPromise: Promise<void> | null = null;

function loadTurnstile(): Promise<void> {
  if (window.turnstile) return Promise.resolve();
  if (scriptPromise) return scriptPromise;
  scriptPromise = new Promise<void>((resolve, reject) => {
    const existing = document.getElementById("cf-turnstile-script") as HTMLScriptElement | null;
    const script = existing || document.createElement("script");
    const loaded = () => {
      if (window.turnstile) resolve();
      else reject(new Error("Turnstile API 未正确加载"));
    };
    script.addEventListener("load", loaded, { once: true });
    script.addEventListener("error", () => reject(new Error("无法加载 Cloudflare Turnstile")), { once: true });
    if (!existing) {
      script.id = "cf-turnstile-script";
      script.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
      script.async = true;
      script.defer = true;
      document.head.appendChild(script);
    } else if (window.turnstile) {
      resolve();
    }
  }).catch((error) => {
    scriptPromise = null;
    throw error;
  });
  return scriptPromise as Promise<void>;
}

export function TurnstileWidget({ siteKey, publicId, onToken, onError, resetSignal }: {
  siteKey: string;
  publicId: string;
  onToken: (token: string) => void;
  onError: (message: string) => void;
  resetSignal: number;
}) {
  const container = useRef<HTMLDivElement>(null);
  const tokenCallback = useRef(onToken);
  const errorCallback = useRef(onError);

  useEffect(() => { tokenCallback.current = onToken; }, [onToken]);
  useEffect(() => { errorCallback.current = onError; }, [onError]);

  useEffect(() => {
    let active = true;
    let widgetId = "";
    loadTurnstile().then(() => {
      if (!active || !container.current || !window.turnstile) return;
      widgetId = window.turnstile.render(container.current, {
        sitekey: siteKey,
        theme: "dark",
        language: "zh-CN",
        action: "flashshare_claim",
        cdata: publicId,
        size: "flexible",
        callback: (token) => {
          if (active) tokenCallback.current(token);
        },
        "expired-callback": () => {
          if (active) {
            tokenCallback.current("");
            errorCallback.current("人机验证已过期，请重新完成验证");
          }
        },
        "timeout-callback": () => {
          if (active) {
            tokenCallback.current("");
            errorCallback.current("人机验证超时，请重新完成验证");
          }
        },
        "error-callback": (code) => {
          if (active) {
            tokenCallback.current("");
            errorCallback.current(`人机验证加载失败${code ? `（${code}）` : ""}`);
          }
        }
      });
    }).catch((error: unknown) => {
      if (active) errorCallback.current(error instanceof Error ? error.message : "无法加载人机验证");
    });
    return () => {
      active = false;
      if (widgetId && window.turnstile) window.turnstile.remove(widgetId);
    };
  }, [publicId, resetSignal, siteKey]);

  return <div className="turnstile-widget" ref={container} />;
}

