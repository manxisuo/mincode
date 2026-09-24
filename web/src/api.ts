import type { ContextSnapshot, MetricsInfo, SessionInfo } from "./types";

export function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  return fetch(url, init).then(async (r) => {
    const data = await r.json().catch(() => ({}));
    if (!r.ok) {
      const err = (data as { error?: string }).error || r.statusText || "request failed";
      throw new Error(err);
    }
    return data as T;
  });
}

export function apiSession() {
  return fetchJSON<SessionInfo>("/api/session");
}

export function apiChat(message: string) {
  return fetchJSON<{ ok: boolean; running?: boolean }>("/api/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
  });
}

export function apiCancel() {
  return fetchJSON<{ ok: boolean; cancelled: boolean }>("/api/cancel", {
    method: "POST",
  });
}

export function apiMetrics() {
  return fetchJSON<MetricsInfo>("/api/metrics");
}

export function apiContext() {
  return fetchJSON<ContextSnapshot>("/api/context");
}

/** T-obs-5: historical context snapshot at a build step. */
export function apiContextAtStep(step: number) {
  return fetchJSON<{
    snapshot: ContextSnapshot | null;
    replay?: boolean;
    step?: number;
    error?: string;
  }>(`/api/context?step=${encodeURIComponent(String(step))}`);
}
