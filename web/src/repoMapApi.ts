import { fetchJSON } from "./api";

export interface RepoMapSymbol {
  kind: string;
  name: string;
  sig?: string;
  line?: number;
}

export interface RepoMapRank {
  defs: number;
  defs_capped?: boolean;
  exported: number;
  refs: number;
  refs_capped?: boolean;
  focus_boost?: number;
  path_hit?: boolean;
  symbol_hits?: number;
  test_penalty?: number;
  total: number;
}

export interface RepoMapFile {
  path: string;
  lang: string;
  package?: string;
  lines?: number;
  symbols?: RepoMapSymbol[];
  score: number;
  rank: RepoMapRank;
  truncated?: boolean;
}

export interface RepoMapResponse {
  enabled: boolean;
  root?: string;
  subpath?: string;
  focus?: string;
  max_tokens?: number;
  files?: RepoMapFile[];
  text?: string;
  tokens?: number;
  scanned?: number;
  skipped?: number;
  truncated?: boolean;
  build_ms?: number;
  cache_hits?: number;
  cache_misses?: number;
}

/** Load the current repository map (optionally scoped to a subdir / focus term). */
export function apiRepoMap(opts?: {
  path?: string;
  focus?: string;
}): Promise<RepoMapResponse> {
  const q = new URLSearchParams();
  if (opts?.path) q.set("path", opts.path);
  if (opts?.focus) q.set("focus", opts.focus);
  const qs = q.toString();
  return fetchJSON<RepoMapResponse>("/api/repomap" + (qs ? `?${qs}` : ""));
}
