import type { HubKind } from './hub-policy';
export interface HubTarget { targetId: string; accountId?: string; accountName: string; providerFamily?: string; groups?: { id: string; name: string }[] }
export interface HubTargetInput { reportId?: string; targetId?: string; groupId?: string; accountName?: string; groupName?: string }
export type HubProtocol = 'chat' | 'openai' | 'anthropic';
export interface HubCatalogAccount { targetId: string; accountId: string; accountName: string; priority?: number; models?: string; protocol?: HubProtocol }
export interface HubCatalogGroup { id: string; name: string; accountsError?: boolean; accounts: HubCatalogAccount[] }
export interface HubCatalog { workspaceId: string; groups: HubCatalogGroup[] }
export interface HubContext extends HubTarget { workspaceId: string; groupId: string; groupName: string; baseURL: string; protocol: string; canRun: boolean; reason?: string; expectedContext: Record<string, string> }
export interface HubSettings { protocol?: string; timeoutSeconds: number; totalTimeoutSeconds: number; concurrency: number; maxOutputTokens: number; retries: number; maxAttempts: number; memoryLimitMiB: number; revision: number }
export interface HubJudge { type: 'legacy' | 'text' | 'number' | 'keywords' | 'model_identity' | 'manual'; expectedText?: string; expectedNumber?: number; expectedFrom?: string; match?: string; required?: string[]; forbidden?: string[]; referenceAnswer?: string }
export interface HubQuestion { id: string; title: string; kind: HubKind; enabled: boolean; order: number; maxScore: number; template: string; parameters: Record<string, unknown>; judge: HubJudge; revision?: number }
export type HubQuestionSummary = Pick<HubQuestion, 'id' | 'title' | 'kind' | 'enabled' | 'order' | 'revision'>;
export interface HubReport { retry?: { id: string; expectedRetryId: string; itemIds: string[]; attemptsBefore: number; acceptedAtMs: number }; reportId: string; targetId: string; accountName: string; accountId?: string; groupId?: string; groupName: string; model: string; protocol?: string; status: string; createdAtMs: number; startedAtMs?: number; updatedAtMs?: number; queuePosition?: number; queuedCount?: number; stopRequested?: boolean; itemCounts?: Record<string, number>; summary?: Record<string, unknown>; settings?: HubSettings; processExited?: boolean; diagnostic?: string; error?: unknown; taskMemoryMaxBytes?: number; sliceMemoryMaxBytes?: number; swapMaxBytes?: number; memoryPeakBytes?: number }
export interface HubItem { tags?: string[]; itemId: string; sectionId: string; orderIndex?: number; usableInjectionPoints?: 0 | 1 | 2; definition: HubQuestion; prompt: string; expected: string; status: string; attempts?: number; startedAtMs?: number; completedAtMs?: number; imageAvailable?: boolean; result?: { answer?: string; earned?: number | null; max?: number; half?: boolean; judgment?: string | { status?: string; reason?: string; earned?: number | null; max?: number; half?: boolean }; error?: string; metrics?: unknown; evidence?: unknown; [key: string]: unknown } }
export interface HubRuntime { enabled: boolean; available: boolean; reason?: string; memoryCeilingBytes?: number; busy?: boolean; queueBlocked?: boolean; queuedCount?: number; active?: HubReport | null }
export interface HubPage<T> { items: T[]; nextCursor?: string }
export type HubStartApproval = (currentProtocol?: HubProtocol) => boolean;
export type HubCall = <T = unknown>(op: string, params?: Record<string, unknown>, approveStart?: HubStartApproval) => Promise<T>;

export interface HubPromptMatch {
  id: string;
  name: string;
  identity: string;
  tokens: number;
  diff: number;
  exact: boolean;
}

export interface HubPrefixPrompt {
  id: string;
  name: string;
  identity: string;
  tokens: number;
  source: string;
  text: string;
}

export type HubCrossCheckGrade = 'A' | 'B' | 'C' | 'D' | 'E';
export type HubVisionStatus = 'full' | 'partial' | 'zero' | 'none';

export interface HubCrossCheck {
  version: 1;
  reportId: string;
  model: string;
  reportStatus: string;
  reportUpdatedAtMs: number;
  terminal: boolean;
  perItemChecked: boolean;
  prefixStatus: 'ok' | 'none' | 'stale' | 'unavailable' | 'invalid';
  prefixReason: string;
  prefixStale: boolean;
  checked: number;
  reference: {
    itemId: string;
    aTotal: number;
    aInput: number;
    aCache: number;
    points: number;
    defaultTemplate: boolean;
    aPromptTokens: number;
    overhead: number;
    prefixTokens: number;
    match: HubPromptMatch | null;
  } | null;
  referenceUnavailableReason: string;
  injectionCount: number;
  injectionPoints: number;
  injectionReliable: boolean | null;
  injectionError: string;
  groups: Array<{
    overhead: number;
    overheadMax: number;
    prefixTokens: number;
    count: number;
    items: Array<{ itemId: string; orderIndex: number; overhead: number }>;
    sameAsReference: boolean;
    match: HubPromptMatch | null;
  }> | null;
  expectedPromptIds: string[];
  expectedPromptNames: string[];
  selfReport: { present: boolean; itemId: string; status: string; earned: number | null; max: number | null; byPrompt: boolean };
  knowledge: { rows: number; peerReports: number; contradictions: Array<{ eventId: string; event: string; date: string; knownBy: number }> };
  vision: { status: HubVisionStatus; peerFull: number };
  codes: Array<'mixed_routes' | 'prompt_mismatch' | 'self_report_gpt5' | 'knowledge_older' | 'vision_mismatch'>;
  grade: HubCrossCheckGrade;
  peers: Array<{
    reportId: string;
    accountName: string;
    createdAtMs: number;
    status: string;
    prefixTokens: number | null;
    latestKnown: { event: string; date: string } | null;
    vision: HubVisionStatus;
  }>;
}
