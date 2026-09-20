export interface RuntimeEvent {
  id?: string;
  time?: string;
  step?: number;
  type: string;
  data?: Record<string, unknown>;
}

export interface ContextItem {
  source: string;
  role?: string;
  preview?: string;
  token_count?: number;
  included?: boolean;
  excluded?: boolean;
  truncated?: boolean;
  pinned?: boolean;
  reason?: string;
  tool_call_id?: string;
}

export interface ContextSnapshot {
  step?: number;
  items?: ContextItem[];
  total_tokens?: number;
  tool_tokens?: number;
  budget?: number;
  included_count?: number;
  excluded_count?: number;
  truncated_count?: number;
}

export interface SessionInfo {
  session_id: string;
  workspace: string;
  provider: string;
  model: string;
  state: string;
  running: boolean;
  last_error?: string;
  turn?: {
    id?: number;
    final?: string;
    steps?: number;
    tool_calls?: number;
  };
  snapshot?: ContextSnapshot | null;
}

export interface MetricsInfo {
  llm_calls?: number;
  errors?: number;
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
  llm_duration_ms?: number;
  parallel_batches?: number;
  parallel_tool_calls?: number;
}

export type PlanStatus =
  | "draft"
  | "approved"
  | "rejected"
  | "running"
  | "done"
  | "failed"
  | "cancelled"
  | "pending"
  | "skipped";

export interface PlanStep {
  index: number;
  title: string;
  status: PlanStatus;
  result?: string;
  error?: string;
}

export interface Plan {
  id: string;
  goal: string;
  steps: PlanStep[];
  status: PlanStatus;
  created_at?: string;
  current?: number;
}

export interface ChatMessage {
  id: string;
  role: "user" | "assistant" | "system";
  text: string;
  /** Local time when the bubble was first shown (ISO string). */
  ts?: string;
}

export interface SkillListItem {
  name: string;
  rel_path: string;
  summary?: string;
  active?: boolean;
  bytes?: number;
}

export interface SkillDetail extends SkillListItem {
  path?: string;
  content: string;
}

export interface PendingPermission {
  id: string;
  tool: string;
  arguments?: string;
  summary?: string;
  diff?: string;
  created_at?: string;
}

export interface SessionListItem {
  id: string;
  title?: string;
  workspace?: string;
  provider?: string;
  model?: string;
  turns?: number;
  message_count?: number;
  updated_at?: string;
  is_current?: boolean;
}

export interface SessionLoadResult {
  ok: boolean;
  id: string;
  provider?: string;
  model?: string;
  turns?: number;
  entries?: number;
  messages: { role: string; content: string }[];
}



