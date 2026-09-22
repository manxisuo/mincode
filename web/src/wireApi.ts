import { fetchJSON } from "./api";

export interface WireMessageView {
  index: number;
  role: string;
  content_len: number;
  content_preview?: string;
  tool_call_id?: string;
  tool_calls?: string[];
  content?: string;
}

export interface WireToolView {
  name: string;
  schema_bytes: number;
  description_len?: number;
}

export interface WireRecord {
  time?: string;
  step?: number;
  provider?: string;
  model?: string;
  stream?: boolean;
  messages?: Array<{
    role: string;
    content?: string;
    tool_call_id?: string;
    tool_calls?: Array<{ id?: string; name?: string; arguments?: string }>;
  }>;
  tools?: WireToolView[];
  temperature?: number;
  max_tokens?: number;
  estimated_prompt_tokens?: number;
  snapshot_total_tokens?: number;
  snapshot_tool_tokens?: number;
  provider_prompt_tokens?: number;
  prompt_token_delta?: number;
  summary?: {
    provider?: string;
    model?: string;
    stream?: boolean;
    message_count?: number;
    tool_count?: number;
    content_bytes?: number;
    schema_bytes?: number;
    messages?: WireMessageView[];
    tools?: WireToolView[];
  };
}

export function apiWire() {
  return fetchJSON<{
    wire: WireRecord | null;
    chain?: Array<{ stage: string; desc: string }>;
    note?: string;
    fetched_at?: string;
  }>("/api/wire");
}
