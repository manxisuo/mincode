import { fetchJSON } from "./api";
import type { SessionListItem, SessionLoadResult } from "./types";

export function apiSessions() {
  return fetchJSON<{
    sessions: SessionListItem[];
    current?: string;
    process_id?: string;
    sessions_dir?: string;
  }>("/api/sessions");
}

export function apiSessionLoad(id: string) {
  return fetchJSON<SessionLoadResult>(
    "/api/sessions/" + encodeURIComponent(id) + "/load",
    { method: "POST" },
  );
}

export function apiSessionUpdate(
  id: string,
  body: { title?: string; note?: string },
) {
  return fetchJSON<{
    ok: boolean;
    id: string;
    title?: string;
    note?: string;
  }>("/api/sessions/" + encodeURIComponent(id), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export function apiSessionDelete(id: string) {
  return fetchJSON<{ ok: boolean; id: string }>(
    "/api/sessions/" + encodeURIComponent(id),
    { method: "DELETE" },
  );
}
