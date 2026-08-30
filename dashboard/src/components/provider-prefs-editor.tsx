"use client";

/**
 * ProviderPrefsEditor — structured form for an OpenRouter `provider` routing
 * object (quick 260830-o2j). Shared by /modelos (per-alias) and
 * /tenants/gerenciar (per-tenant). Mirrors the reference shape from Pedro's
 * screenshot (2026-08-30):
 *
 *   only / order / allow_fallbacks / quantizations / max_price
 *   preferred_min_throughput / preferred_max_latency / data_collection / zdr
 *
 * plus the remaining documented fields (ignore / require_parameters / sort).
 * The form is a THIN view over a `ProviderPrefs` value: every control edits
 * the object; a live JSON preview shows exactly what the gateway will inject.
 * Validation is `validateProviderPrefs` (same rules as the gateway).
 */

import { useMemo, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  PROVIDER_QUANTIZATIONS,
  PROVIDER_SORT_BY,
  type ProviderPrefs,
  validateProviderPrefs,
} from "@/lib/provider-prefs";

export type ProviderPrefsDraft = ProviderPrefs;

function listToText(v?: string[]): string {
  return v ? v.join(", ") : "";
}
function textToList(s: string): string[] | undefined {
  const items = s
    .split(/[,\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
  return items.length ? items : undefined;
}
function numOrUndef(s: string): number | undefined {
  if (s.trim() === "") return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}
function p90Of(t: ProviderPrefs["preferred_max_latency"]): string {
  if (t === undefined) return "";
  if (typeof t === "number") return String(t);
  return t.p90 !== undefined ? String(t.p90) : "";
}

/** Tri-state select value for optional booleans. */
type Tri = "unset" | "true" | "false";
function triOf(v: boolean | undefined): Tri {
  return v === undefined ? "unset" : v ? "true" : "false";
}
function ofTri(t: Tri): boolean | undefined {
  return t === "unset" ? undefined : t === "true";
}

export function ProviderPrefsEditor({
  value,
  onChange,
  disabled,
}: {
  value: ProviderPrefs;
  onChange: (next: ProviderPrefs) => void;
  disabled?: boolean;
}) {
  const [onlyText, setOnlyText] = useState(listToText(value.only));
  const [orderText, setOrderText] = useState(listToText(value.order));
  const [ignoreText, setIgnoreText] = useState(listToText(value.ignore));

  const set = <K extends keyof ProviderPrefs>(k: K, v: ProviderPrefs[K]) => {
    const next = { ...value };
    if (v === undefined) delete next[k];
    else next[k] = v;
    onChange(next);
  };

  const validation = useMemo(() => {
    try {
      const v = validateProviderPrefs(value);
      return { ok: true as const, json: JSON.stringify(v, null, 2) };
    } catch (e) {
      return { ok: false as const, error: (e as Error).message, json: JSON.stringify(value, null, 2) };
    }
  }, [value]);

  const sortBy = typeof value.sort === "string" ? value.sort : value.sort?.by;

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <div className="flex flex-col gap-3">
        <Field label="only (só estes provedores)" hint="separar por vírgula — ex.: novita, deepinfra">
          <Input
            disabled={disabled}
            value={onlyText}
            placeholder="provedor-a, provedor-b"
            onChange={(e) => {
              setOnlyText(e.target.value);
              set("only", textToList(e.target.value));
            }}
          />
        </Field>
        <Field label="order (prioridade)" hint="tentados nesta ordem">
          <Input
            disabled={disabled}
            value={orderText}
            placeholder="provedor-a, provedor-b"
            onChange={(e) => {
              setOrderText(e.target.value);
              set("order", textToList(e.target.value));
            }}
          />
        </Field>
        <Field label="ignore (nunca usar)">
          <Input
            disabled={disabled}
            value={ignoreText}
            placeholder="provedor-x"
            onChange={(e) => {
              setIgnoreText(e.target.value);
              set("ignore", textToList(e.target.value));
            }}
          />
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label="allow_fallbacks">
            <TriSelect
              disabled={disabled}
              value={triOf(value.allow_fallbacks)}
              onChange={(t) => set("allow_fallbacks", ofTri(t))}
            />
          </Field>
          <Field label="require_parameters">
            <TriSelect
              disabled={disabled}
              value={triOf(value.require_parameters)}
              onChange={(t) => set("require_parameters", ofTri(t))}
            />
          </Field>
          <Field label="data_collection">
            <Select
              disabled={disabled}
              value={value.data_collection ?? "unset"}
              onValueChange={(v) =>
                set("data_collection", v === "unset" ? undefined : (v as "allow" | "deny"))
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="unset">(padrão OpenRouter)</SelectItem>
                <SelectItem value="allow">allow</SelectItem>
                <SelectItem value="deny">deny</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label="sort">
            <Select
              disabled={disabled}
              value={sortBy ?? "unset"}
              onValueChange={(v) =>
                set("sort", v === "unset" ? undefined : (v as (typeof PROVIDER_SORT_BY)[number]))
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="unset">(padrão)</SelectItem>
                {PROVIDER_SORT_BY.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>

        <div className="flex items-center justify-between rounded-md border border-border px-3 py-2">
          <div className="flex flex-col">
            <span className="text-[13px] font-medium">zdr</span>
            <span className="text-[11px] text-muted-foreground">
              só provedores Zero Data Retention
            </span>
          </div>
          <Switch
            disabled={disabled}
            checked={value.zdr === true}
            onCheckedChange={(c) => set("zdr", c ? true : undefined)}
          />
        </div>

        <Field label="quantizations">
          <div className="flex flex-wrap gap-1.5">
            {PROVIDER_QUANTIZATIONS.map((q) => {
              const on = value.quantizations?.includes(q) ?? false;
              return (
                <button
                  key={q}
                  type="button"
                  disabled={disabled}
                  onClick={() => {
                    const cur = new Set(value.quantizations ?? []);
                    if (on) cur.delete(q);
                    else cur.add(q);
                    const arr = PROVIDER_QUANTIZATIONS.filter((x) => cur.has(x));
                    set("quantizations", arr.length ? [...arr] : undefined);
                  }}
                  className="rounded-full"
                >
                  <Badge variant={on ? "default" : "outline"} className="cursor-pointer font-mono">
                    {q}
                  </Badge>
                </button>
              );
            })}
          </div>
        </Field>

        <div className="grid grid-cols-2 gap-3">
          <Field label="max_price.prompt ($/M)">
            <Input
              disabled={disabled}
              type="number"
              min={0}
              step="0.01"
              value={value.max_price?.prompt ?? ""}
              onChange={(e) => {
                const mp = { ...(value.max_price ?? {}) };
                const n = numOrUndef(e.target.value);
                if (n === undefined) delete mp.prompt;
                else mp.prompt = n;
                set("max_price", Object.keys(mp).length ? mp : undefined);
              }}
            />
          </Field>
          <Field label="max_price.completion ($/M)">
            <Input
              disabled={disabled}
              type="number"
              min={0}
              step="0.01"
              value={value.max_price?.completion ?? ""}
              onChange={(e) => {
                const mp = { ...(value.max_price ?? {}) };
                const n = numOrUndef(e.target.value);
                if (n === undefined) delete mp.completion;
                else mp.completion = n;
                set("max_price", Object.keys(mp).length ? mp : undefined);
              }}
            />
          </Field>
          <Field label="preferred_min_throughput p90 (tok/s)">
            <Input
              disabled={disabled}
              type="number"
              min={0}
              value={p90Of(value.preferred_min_throughput)}
              onChange={(e) => {
                const n = numOrUndef(e.target.value);
                set("preferred_min_throughput", n === undefined ? undefined : { p90: n });
              }}
            />
          </Field>
          <Field label="preferred_max_latency p90 (s)">
            <Input
              disabled={disabled}
              type="number"
              min={0}
              step="0.1"
              value={p90Of(value.preferred_max_latency)}
              onChange={(e) => {
                const n = numOrUndef(e.target.value);
                set("preferred_max_latency", n === undefined ? undefined : { p90: n });
              }}
            />
          </Field>
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <span className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
            JSON enviado ao OpenRouter
          </span>
          {validation.ok ? (
            <Badge variant="default">válido</Badge>
          ) : (
            <Badge variant="destructive">inválido</Badge>
          )}
        </div>
        <pre className="min-h-[220px] overflow-auto rounded-md border border-border bg-muted/40 p-3 font-mono text-[12px] leading-relaxed">
          {`"provider": ${validation.json}`}
        </pre>
        {!validation.ok && (
          <p className="text-[12px] text-destructive" role="alert">
            {validation.error}
          </p>
        )}
        {!disabled && (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="self-start"
            onClick={() => {
              setOnlyText("");
              setOrderText("");
              setIgnoreText("");
              onChange({});
            }}
          >
            Limpar tudo
          </Button>
        )}
      </div>
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[12px] font-semibold text-muted-foreground">{label}</span>
      {children}
      {hint && <span className="text-[11px] text-muted-foreground">{hint}</span>}
    </label>
  );
}

function TriSelect({
  value,
  onChange,
  disabled,
}: {
  value: Tri;
  onChange: (t: Tri) => void;
  disabled?: boolean;
}) {
  return (
    <Select disabled={disabled} value={value} onValueChange={(v) => onChange(v as Tri)}>
      <SelectTrigger>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="unset">(padrão)</SelectItem>
        <SelectItem value="true">true</SelectItem>
        <SelectItem value="false">false</SelectItem>
      </SelectContent>
    </Select>
  );
}
