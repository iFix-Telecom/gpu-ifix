/**
 * OpenRouter provider-routing preferences — shared TS mirror of the gateway's
 * `models.ValidateProviderPrefs` (quick 260830-o2j).
 *
 * Used by BOTH the client editor (instant feedback) and the owner server
 * actions (defense-in-depth before the gateway call; the gateway re-validates
 * anyway — it is the authority). Schema follows
 * https://openrouter.ai/docs/features/provider-routing verbatim.
 *
 * Precedence at request time (decided 2026-08-30): tenant.provider_prefs >
 * model_aliases.provider_prefs (alias, openrouter-chat) > global env pin.
 */

export const PROVIDER_QUANTIZATIONS = [
  "int4",
  "int8",
  "fp4",
  "mxfp4",
  "nvfp4",
  "fp6",
  "fp8",
  "mxfp8",
  "fp16",
  "bf16",
  "fp32",
  "unknown",
] as const;
export type ProviderQuantization = (typeof PROVIDER_QUANTIZATIONS)[number];

export const PROVIDER_SORT_BY = ["price", "throughput", "latency"] as const;
export type ProviderSortBy = (typeof PROVIDER_SORT_BY)[number];

export const PROVIDER_PERCENTILES = ["p50", "p75", "p90", "p99"] as const;
export type ProviderPercentile = (typeof PROVIDER_PERCENTILES)[number];

export type ProviderThreshold = number | Partial<Record<ProviderPercentile, number>>;

/** The `provider` object as stored on the gateway and sent to OpenRouter. */
export interface ProviderPrefs {
  only?: string[];
  order?: string[];
  ignore?: string[];
  allow_fallbacks?: boolean;
  require_parameters?: boolean;
  data_collection?: "allow" | "deny";
  zdr?: boolean;
  enforce_distillable_text?: boolean;
  quantizations?: ProviderQuantization[];
  sort?: ProviderSortBy | { by: ProviderSortBy; partition?: "model" | "none" };
  max_price?: { prompt?: number; completion?: number; request?: number; image?: number };
  preferred_min_throughput?: ProviderThreshold;
  preferred_max_latency?: ProviderThreshold;
}

const KNOWN_KEYS = new Set<keyof ProviderPrefs>([
  "only",
  "order",
  "ignore",
  "allow_fallbacks",
  "require_parameters",
  "data_collection",
  "zdr",
  "enforce_distillable_text",
  "quantizations",
  "sort",
  "max_price",
  "preferred_min_throughput",
  "preferred_max_latency",
]);

export const PROVIDER_PREFS_MAX_BYTES = 4096;

class PrefsError extends Error {}

function fail(msg: string): never {
  throw new PrefsError(`provider_prefs: ${msg}`);
}

function slugList(name: string, v: unknown, allowed?: readonly string[]): string[] {
  if (!Array.isArray(v)) fail(`${name} deve ser uma lista`);
  if (v.length === 0) fail(`${name} não pode ser lista vazia (remova o campo)`);
  if (v.length > 32) fail(`${name} tem entradas demais (máx 32)`);
  return v.map((s) => {
    if (typeof s !== "string" || s === "" || /\s/.test(s) || s.length > 64) {
      fail(`${name}: entrada inválida ${JSON.stringify(s)}`);
    }
    if (allowed && !allowed.includes(s)) {
      fail(`${name}: ${JSON.stringify(s)} não é um de ${allowed.join(",")}`);
    }
    return s;
  });
}

function nonNeg(name: string, v: unknown): number {
  if (typeof v !== "number" || !Number.isFinite(v) || v < 0) {
    fail(`${name} deve ser número >= 0`);
  }
  return v;
}

function bool(name: string, v: unknown): boolean {
  if (typeof v !== "boolean") fail(`${name} deve ser true/false`);
  return v;
}

function threshold(name: string, v: unknown): ProviderThreshold {
  if (typeof v === "number") return nonNeg(name, v);
  if (v && typeof v === "object" && !Array.isArray(v)) {
    const entries = Object.entries(v as Record<string, unknown>);
    if (entries.length === 0) fail(`${name} não pode ser objeto vazio`);
    const out: Partial<Record<ProviderPercentile, number>> = {};
    for (const [k, val] of entries) {
      if (!(PROVIDER_PERCENTILES as readonly string[]).includes(k)) {
        fail(`${name}: percentil desconhecido ${JSON.stringify(k)} (use p50/p75/p90/p99)`);
      }
      out[k as ProviderPercentile] = nonNeg(`${name}.${k}`, val);
    }
    return out;
  }
  fail(`${name} deve ser número ou {p50|p75|p90|p99: número}`);
}

/**
 * Validate + normalize an arbitrary value into a `ProviderPrefs`. Throws an
 * Error with a pt-BR message on the first violation. Returns `null` for
 * `null`/`undefined` (= clear). An empty object is rejected.
 */
export function validateProviderPrefs(input: unknown): ProviderPrefs | null {
  if (input === null || input === undefined) return null;
  if (typeof input !== "object" || Array.isArray(input)) fail("deve ser um objeto JSON");
  const raw = input as Record<string, unknown>;
  const out: ProviderPrefs = {};
  for (const key of Object.keys(raw)) {
    if (!KNOWN_KEYS.has(key as keyof ProviderPrefs)) fail(`campo desconhecido ${JSON.stringify(key)}`);
    const v = raw[key];
    if (v === undefined || v === null) continue;
    switch (key as keyof ProviderPrefs) {
      case "only":
      case "order":
      case "ignore":
        out[key as "only" | "order" | "ignore"] = slugList(key, v);
        break;
      case "quantizations":
        out.quantizations = slugList(key, v, PROVIDER_QUANTIZATIONS) as ProviderQuantization[];
        break;
      case "allow_fallbacks":
      case "require_parameters":
      case "zdr":
      case "enforce_distillable_text":
        out[key as "allow_fallbacks" | "require_parameters" | "zdr" | "enforce_distillable_text"] =
          bool(key, v);
        break;
      case "data_collection":
        if (v !== "allow" && v !== "deny") fail('data_collection deve ser "allow" ou "deny"');
        out.data_collection = v;
        break;
      case "sort": {
        if (typeof v === "string") {
          if (!(PROVIDER_SORT_BY as readonly string[]).includes(v)) {
            fail("sort deve ser price|throughput|latency");
          }
          out.sort = v as ProviderSortBy;
        } else if (v && typeof v === "object" && !Array.isArray(v)) {
          const o = v as Record<string, unknown>;
          for (const k of Object.keys(o)) {
            if (k !== "by" && k !== "partition") fail(`sort: campo desconhecido ${JSON.stringify(k)}`);
          }
          if (!(PROVIDER_SORT_BY as readonly string[]).includes(o.by as string)) {
            fail("sort.by deve ser price|throughput|latency");
          }
          const sort: { by: ProviderSortBy; partition?: "model" | "none" } = {
            by: o.by as ProviderSortBy,
          };
          if (o.partition !== undefined) {
            if (o.partition !== "model" && o.partition !== "none") fail("sort.partition deve ser model|none");
            sort.partition = o.partition;
          }
          out.sort = sort;
        } else {
          fail("sort inválido");
        }
        break;
      }
      case "max_price": {
        if (!v || typeof v !== "object" || Array.isArray(v)) fail("max_price deve ser objeto");
        const o = v as Record<string, unknown>;
        const mp: NonNullable<ProviderPrefs["max_price"]> = {};
        for (const k of Object.keys(o)) {
          if (!["prompt", "completion", "request", "image"].includes(k)) {
            fail(`max_price: campo desconhecido ${JSON.stringify(k)}`);
          }
          if (o[k] === undefined || o[k] === null) continue;
          mp[k as keyof typeof mp] = nonNeg(`max_price.${k}`, o[k]);
        }
        if (Object.keys(mp).length === 0) fail("max_price precisa de prompt/completion/request/image");
        out.max_price = mp;
        break;
      }
      case "preferred_min_throughput":
      case "preferred_max_latency":
        out[key as "preferred_min_throughput" | "preferred_max_latency"] = threshold(key, v);
        break;
    }
  }
  if (Object.keys(out).length === 0) fail("defina ao menos um campo (ou limpe para remover)");
  const size = JSON.stringify(out).length;
  if (size > PROVIDER_PREFS_MAX_BYTES) fail(`excede ${PROVIDER_PREFS_MAX_BYTES} bytes`);
  return out;
}

/** Short human summary for tables ("only: novita,deepinfra · zdr · ≤$0.14/$0.50"). */
export function summarizeProviderPrefs(p: ProviderPrefs | null | undefined): string {
  if (!p) return "—";
  const parts: string[] = [];
  if (p.only) parts.push(`only: ${p.only.join(",")}`);
  if (p.order) parts.push(`order: ${p.order.join(",")}`);
  if (p.ignore) parts.push(`ignore: ${p.ignore.join(",")}`);
  if (p.allow_fallbacks === false) parts.push("sem fallback");
  if (p.require_parameters) parts.push("require_params");
  if (p.data_collection === "deny") parts.push("no-collect");
  if (p.zdr) parts.push("zdr");
  if (p.quantizations) parts.push(`quant: ${p.quantizations.join(",")}`);
  if (p.sort) parts.push(`sort: ${typeof p.sort === "string" ? p.sort : p.sort.by}`);
  if (p.max_price) {
    const mp = p.max_price;
    parts.push(`≤$${mp.prompt ?? "∞"}/${mp.completion ?? "∞"}`);
  }
  if (p.preferred_min_throughput !== undefined) parts.push("min tok/s");
  if (p.preferred_max_latency !== undefined) parts.push("max lat");
  return parts.length ? parts.join(" · ") : "—";
}
