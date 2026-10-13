"use client";

// Shared pieces for LLM retry config: each of the five layers' 'count + interval'.
//
// Five layers inside-out: connect (SDK) → empty response (SDK) → same-provider safety window → polling breaker → intent rerun.
// The first three layers follow the endpoint, so each model config can override the global default; the last two are process-level with a single global copy.
//
// All inputs follow the same 'leave empty = unconfigured' semantics, matching backend db.RetryRule:
//   timescount   empty/0 = usebuilt-in default | -1 = Offthis layerRetry | >0 = usethiscount
//   interval   empty/0 = usethis layeroriginalthis pointcountbackoff | >0 = changeusethisfixed msinterval

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** Where this layer's retries happen and who runs them */
  where: string;
  /** What errors reach this layer — down to status codes, no guessing */
  trigger: string;
  /** Errors that look like but don't reach this layer, so a fill that does nothing isn't mistaken for a bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** Default when count is left empty, used for placeholders */
  defAttempts: number;
  /** Default policy when interval is left empty, used for placeholders */
  defInterval: string;
  /** Meaning of filling -1 for count */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Connection retries",
    where: "SDK · before getting 200",
    trigger:
      "No connection or no 200 yet: reset connections / read-write timeouts / DNS failures and other network-layer errors, plus HTTP 408, 429, 500, 502, 503, 504.",
    skips: "Other status codes (400 / 401 / 403 / 404 / 413 / 422 etc.) are deterministic rejections; resending fails the same way, so they are raised directly.",
    desc: "Resend the identical request. Once the stream has started (200 received), mid-stream drops are out of this layer's scope.",
    attemptsLabel: "Retry count",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = never retry; failures are raised immediately",
  },
  empty: {
    title: "Empty-response retries",
    where: "SDK · openai format only",
    trigger:
      "HTTP 200 with a normal stop finish_reason, but the whole response has zero content blocks — gateway empty frames, dropped thinking fields, or sampling hiccups all look like this.",
    skips: "Truncation by max_tokens with no content does not count (raise the output limit instead; resending just hits it again).",
    desc: "It resends the whole prompt, so it is expensive on long contexts; keep the count low.",
    attemptsLabel: "Retry count",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = hand over empty responses as-is",
  },
  stream: {
    title: "Same-provider safety-window retries",
    where: "This project · before any output is delivered",
    trigger:
      "Problems only after the stream is established (200 received): mid-stream drops, provider overloaded, in-stream 429 / 5xx error events — with zero tokens delivered to the caller yet.",
    skips:
      "Quota exhaustion (402 / insufficient_quota, handled by swapping configs), overlong context (413 / context length, handled by compression), and deterministic 400 / 401 / 403 / 404 / 422 rejections are never retried.",
    desc: "Replay the same request on the same config. Nothing has been delivered yet, so replaying won't duplicate model output or tool execution.",
    attemptsLabel: "Retry count",
    defAttempts: 2,
    defInterval: "0.5s→1s exponential (capped at 4s)",
    offHint: "-1 = hand broken streams directly to the outer intent rerun",
  },
  breaker: {
    title: "Polling circuit breaker",
    where: "This project · process-level, single global instance",
    trigger:
      "Transient failures (429, 5xx, network errors) trip the breaker after accumulating to the threshold; deterministic failures like insufficient balance (402), dead keys (401 / 403), or missing models (404) trip it on the first occurrence.",
    skips: "One success resets the count, so occasionally flaky configs never accumulate to a trip.",
    desc: "After tripping, the config cools down; polling skips it during cooldown. State is persisted, surviving restarts.",
    attemptsLabel: "Consecutive failures to trip",
    defAttempts: 3,
    defInterval: "1min→5min→30min ramp",
    offHint: "-1 = transient failures never trip (deterministic failures still trip)",
  },
  intent: {
    title: "Intent rerun",
    where: "This project · process-level, single global instance",
    trigger:
      "All inner layers exhausted: the worker ends with model_error — inner retries all used up, or the stream broke after output delivery started (replay is unsafe then; the whole intent must rerun).",
    skips: "Quota exhaustion is already handled by config rotation, not rerun here; when the task is paused / terminated / wrapping up, yield immediately without consuming backoff.",
    desc: "Rerun the whole intent from scratch. It is the outermost layer, so one rerun multiplies all inner-layer counts again.",
    attemptsLabel: "Rerun count",
    defAttempts: 2,
    defInterval: "Fixed 3s",
    offHint: "-1 = no rerun; the intent is marked blocked directly",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Human-friendly milliseconds, shown only next to the input to avoid a bare count. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Controlled digit input: empty string ↔ 0; intermediate states ("-", "1e") stay as-is locally without disturbing the parent. */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // Follows when the parent swaps in a whole value set (reading a policy, switching configs); not while typing locally,
  // because then the value already equals the locally parsed text.
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** One layer's two knobs. idPrefix keeps label htmlFor unique when appearing multiple times on one page. */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = compact version in the config drawer: drops the expandable notes, keeping only 'which errors reach this layer'. */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* Which errors reach this layer, down to status codes — if the knobs seem to do nothing, the error probably never lands here. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">trigger</span>:{meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">notgothis layer</span>:{meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Default ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            interval ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="Default backoff"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Fixed ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">Leave empty = default;{meta.offHint}.</p>}
    </div>
  );
}

/** Three-layer overrides in the model config drawer (the three endpoint-following layers). */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">Retrycover</Label>
        <p className="text-muted-foreground text-xs">
          Takes effect for this config only, overriding the global 'retry and backoff' defaults. Empty = follow global; count -1 = turn off this layer;
          A filled interval replaces count backoff with a fixed interval. Breaker and intent rerun are process-level, tunable only on the global page.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** 'Retry and backoff' tab: global defaults for the five layers. */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Read retry policy failed: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // The backend clamps out-of-range values and returns them; refresh with the returned values — what you see is what's stored.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Saved, effective immediately (the in-flight round still uses old parameters)");
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> readfetchRetrypolicy...
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        One failed model call retries through the five layers inside-out:
        <span className="text-foreground"> connect → empty response → same-provider safety window → polling breaker → intent rerun</span>
        Inner layers exhaust before outer ones run; counts
        <span className="text-foreground">multiply</span>
        : maxing every layer means one hiccup can burn dozens of requests.
        All empty = current defaults, fully consistent with this page's behavior. The first three layers can be overridden per model config.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          Save
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          AllRestoreDefault
        </Button>
      </div>
    </div>
  );
}
