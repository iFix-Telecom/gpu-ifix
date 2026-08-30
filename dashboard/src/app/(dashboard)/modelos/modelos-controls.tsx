"use client";

/**
 * Client island for /modelos (quick 260830-o2j).
 *
 * (1) ALIASES — grouped by alias; each row = one (alias, upstream_name) with
 *     role / target / provider-prefs summary. Owner: "Novo alias", edit
 *     (dialog: target + ProviderPrefsEditor when upstream = openrouter-chat),
 *     delete (alert-dialog with impact copy).
 * (2) UPSTREAMS — every upstream with tier/role/probe; owner toggles
 *     `enabled` (Switch → confirm when disabling). The gateway refuses to
 *     disable the last enabled upstream of a role (409 → toast).
 *
 * Precedence reminder shown in the header: tenant prefs > alias prefs >
 * global env pin.
 */

import { Pencil, Plus, Trash2 } from "lucide-react";
import { Fragment, useMemo, useState } from "react";
import { toast } from "sonner";

import { ProviderPrefsEditor } from "@/components/provider-prefs-editor";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  deleteModelAlias,
  setUpstreamEnabled,
  upsertModelAlias,
} from "@/lib/admin-actions";
import {
  fetchModelAliases,
  fetchUpstreams,
  type ModelAliasRow,
  type UpstreamRow,
} from "@/lib/gateway";
import {
  type ProviderPrefs,
  summarizeProviderPrefs,
  validateProviderPrefs,
} from "@/lib/provider-prefs";

const GENERIC_ERROR =
  "Não foi possível concluir a ação agora. Tente novamente em alguns segundos.";
const PREFS_UPSTREAM = "openrouter-chat";

function fmtDate(iso: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" });
}

function probeVariant(status: string | null): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "ok":
      return "default";
    case "config":
      return "outline";
    case "failed":
    case "timeout":
      return "destructive";
    default:
      return "secondary";
  }
}

export function ModelosControls({
  isOwner,
  initialAliases,
  initialUpstreams,
}: {
  isOwner: boolean;
  initialAliases: ModelAliasRow[];
  initialUpstreams: UpstreamRow[];
}) {
  const [aliases, setAliases] = useState(initialAliases);
  const [upstreams, setUpstreams] = useState(initialUpstreams);
  const [editing, setEditing] = useState<ModelAliasRow | "new" | null>(null);
  const [deleting, setDeleting] = useState<ModelAliasRow | null>(null);
  const [disabling, setDisabling] = useState<UpstreamRow | null>(null);
  const [togglingName, setTogglingName] = useState<string | null>(null);

  const grouped = useMemo(() => {
    const m = new Map<string, ModelAliasRow[]>();
    for (const r of aliases) {
      const arr = m.get(r.alias) ?? [];
      arr.push(r);
      m.set(r.alias, arr);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [aliases]);

  async function refreshAliases() {
    try {
      setAliases(await fetchModelAliases());
    } catch (err) {
      toast.error((err as Error)?.message ?? GENERIC_ERROR);
    }
  }
  async function refreshUpstreams() {
    try {
      setUpstreams(await fetchUpstreams());
    } catch (err) {
      toast.error((err as Error)?.message ?? GENERIC_ERROR);
    }
  }

  async function applyToggle(u: UpstreamRow, enabled: boolean) {
    setTogglingName(u.name);
    try {
      await setUpstreamEnabled({ name: u.name, enabled });
      toast.success(`${u.name} ${enabled ? "habilitado" : "desabilitado"}.`);
      await refreshUpstreams();
    } catch (err) {
      toast.error((err as Error)?.message ?? GENERIC_ERROR);
    } finally {
      setTogglingName(null);
      setDisabling(null);
    }
  }

  return (
    <div className="flex flex-col gap-8">
      {/* (1) ALIASES */}
      <Card>
        <CardHeader className="flex flex-row items-start justify-between gap-4">
          <div className="flex flex-col gap-1">
            <CardTitle className="text-[20px] font-semibold">Aliases de modelo</CardTitle>
            <CardDescription>
              Um alias com linhas fica <em>pinado</em> nos upstreams listados (ordem de
              tier). Roteamento OpenRouter: prefs do <strong>tenant</strong> &gt; prefs do{" "}
              <strong>alias</strong> (openrouter-chat) &gt; pin global do env.
            </CardDescription>
          </div>
          {isOwner && (
            <Button type="button" size="sm" onClick={() => setEditing("new")}>
              <Plus className="size-4" />
              Novo alias
            </Button>
          )}
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Alias</TableHead>
                <TableHead>Upstream</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Provider prefs (OpenRouter)</TableHead>
                {isOwner && <TableHead aria-label="ações" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {grouped.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={isOwner ? 6 : 5} className="text-center text-muted-foreground">
                    Nenhum alias cadastrado.
                  </TableCell>
                </TableRow>
              ) : (
                grouped.map(([alias, rows]) => (
                  <Fragment key={alias}>
                    {rows.map((r, i) => (
                      <TableRow key={`${r.alias}:${r.upstream_name}`} data-testid="alias-row">
                        <TableCell className="font-mono text-[13px] font-semibold">
                          {i === 0 ? alias : ""}
                        </TableCell>
                        <TableCell className="font-mono text-[13px]">{r.upstream_name}</TableCell>
                        <TableCell>
                          <Badge variant="outline">{r.role}</Badge>
                        </TableCell>
                        <TableCell className="font-mono text-[13px]">{r.target}</TableCell>
                        <TableCell className="max-w-[320px] truncate text-[12px] text-muted-foreground" title={r.provider_prefs ? JSON.stringify(r.provider_prefs) : undefined}>
                          {r.upstream_name === PREFS_UPSTREAM ? summarizeProviderPrefs(r.provider_prefs) : <span className="opacity-50">n/a</span>}
                        </TableCell>
                        {isOwner && (
                          <TableCell className="text-right">
                            <Button type="button" size="icon" variant="ghost" aria-label={`editar ${r.alias} ${r.upstream_name}`} onClick={() => setEditing(r)}>
                              <Pencil className="size-4" />
                            </Button>
                            <Button type="button" size="icon" variant="ghost" aria-label={`excluir ${r.alias} ${r.upstream_name}`} onClick={() => setDeleting(r)}>
                              <Trash2 className="size-4 text-destructive" />
                            </Button>
                          </TableCell>
                        )}
                      </TableRow>
                    ))}
                  </Fragment>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {/* (2) UPSTREAMS */}
      <Card>
        <CardHeader>
          <CardTitle className="text-[20px] font-semibold">Upstreams</CardTitle>
          <CardDescription>
            Ligar/desligar recarrega o dispatcher na hora (NOTIFY). O gateway recusa
            desligar o único upstream habilitado de um role.
          </CardDescription>
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Nome</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Tier</TableHead>
                <TableHead>URL env</TableHead>
                <TableHead>Probe</TableHead>
                <TableHead>Último probe</TableHead>
                <TableHead>Habilitado</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {upstreams.map((u) => (
                <TableRow key={u.name} data-testid="upstream-row">
                  <TableCell className="font-mono text-[13px]">{u.name}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{u.role}</Badge>
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {u.tier}
                    {u.tier_priority ? <span className="text-muted-foreground">.{u.tier_priority}</span> : null}
                  </TableCell>
                  <TableCell className="font-mono text-[12px] text-muted-foreground">
                    {u.url_env}
                    {u.has_auth ? " 🔑" : ""}
                  </TableCell>
                  <TableCell>
                    <Badge variant={probeVariant(u.last_probe_status)} title={u.last_probe_error ?? undefined}>
                      {u.last_probe_status ?? "—"}
                      {u.last_probe_ms !== null ? ` · ${u.last_probe_ms}ms` : ""}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-[12px] text-muted-foreground">{fmtDate(u.last_probe_at)}</TableCell>
                  <TableCell>
                    <Switch
                      aria-label={`habilitar ${u.name}`}
                      checked={u.enabled}
                      disabled={!isOwner || togglingName === u.name}
                      onCheckedChange={(c) => {
                        if (c) void applyToggle(u, true);
                        else setDisabling(u);
                      }}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {isOwner && editing !== null && (
        <AliasDialog
          row={editing === "new" ? null : editing}
          upstreams={upstreams}
          onClose={() => setEditing(null)}
          onSaved={refreshAliases}
        />
      )}

      {isOwner && (
        <AlertDialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Excluir linha do alias?</AlertDialogTitle>
              <AlertDialogDescription>
                Remove <code>{deleting?.alias}</code> → <code>{deleting?.upstream_name}</code>.
                Se for a última linha do alias, ele deixa de ser pinado e volta à cascata
                por tier; se houver outras, o alias deixa de usar este upstream. Efeito
                imediato neste gateway.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancelar</AlertDialogCancel>
              <AlertDialogAction
                className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                onClick={async (e) => {
                  e.preventDefault();
                  if (!deleting) return;
                  try {
                    await deleteModelAlias({ alias: deleting.alias, upstreamName: deleting.upstream_name });
                    toast.success("Linha removida.");
                    setDeleting(null);
                    await refreshAliases();
                  } catch (err) {
                    toast.error((err as Error)?.message ?? GENERIC_ERROR);
                  }
                }}
              >
                Excluir
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}

      {isOwner && (
        <AlertDialog open={disabling !== null} onOpenChange={(o) => !o && setDisabling(null)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Desabilitar {disabling?.name}?</AlertDialogTitle>
              <AlertDialogDescription>
                O upstream sai da cascata do role <code>{disabling?.role}</code> imediatamente.
                Requests passam a usar o próximo upstream habilitado (ou falham se não houver).
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancelar</AlertDialogCancel>
              <AlertDialogAction
                className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                onClick={(e) => {
                  e.preventDefault();
                  if (disabling) void applyToggle(disabling, false);
                }}
              >
                Desabilitar
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// Alias create/edit dialog — target + (openrouter-chat only) provider prefs.
// ──────────────────────────────────────────────────────────────────────────

function AliasDialog({
  row,
  upstreams,
  onClose,
  onSaved,
}: {
  row: ModelAliasRow | null;
  upstreams: UpstreamRow[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const isEdit = row !== null;
  const [alias, setAlias] = useState(row?.alias ?? "");
  const [upstreamName, setUpstreamName] = useState(row?.upstream_name ?? PREFS_UPSTREAM);
  const [target, setTarget] = useState(row?.target ?? "");
  const [prefsEnabled, setPrefsEnabled] = useState(Boolean(row?.provider_prefs));
  const [prefs, setPrefs] = useState<ProviderPrefs>(row?.provider_prefs ?? {});
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const isOpenRouter = upstreamName === PREFS_UPSTREAM;

  async function save() {
    setError(null);
    let providerPrefs: ProviderPrefs | null | undefined;
    if (isOpenRouter && prefsEnabled) {
      try {
        providerPrefs = validateProviderPrefs(prefs);
      } catch (e) {
        setError((e as Error).message);
        return;
      }
    } else {
      providerPrefs = null;
    }
    setPending(true);
    try {
      await upsertModelAlias({ alias, upstreamName, target, providerPrefs });
      toast.success(isEdit ? "Alias atualizado." : "Alias criado.");
      await onSaved();
      onClose();
    } catch (e) {
      setError((e as Error)?.message ?? GENERIC_ERROR);
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto" style={{ maxWidth: 920 }}>
        <DialogHeader>
          <DialogTitle>{isEdit ? `Editar ${row.alias} → ${row.upstream_name}` : "Novo alias"}</DialogTitle>
          <DialogDescription>
            O alias é o nome que o cliente manda em <code>model</code>; o target é o que o
            upstream recebe. Prefs de provedor valem só para <code>openrouter-chat</code>.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-3 md:grid-cols-3">
          <label className="flex flex-col gap-1">
            <span className="text-[12px] font-semibold text-muted-foreground">Alias</span>
            <Input value={alias} disabled={isEdit} onChange={(e) => setAlias(e.target.value)} placeholder="ex.: gpt-4o" />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] font-semibold text-muted-foreground">Upstream</span>
            <Select value={upstreamName} disabled={isEdit} onValueChange={setUpstreamName}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {upstreams.map((u) => (
                  <SelectItem key={u.name} value={u.name}>
                    {u.name} <span className="text-muted-foreground">({u.role} t{u.tier})</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[12px] font-semibold text-muted-foreground">Target</span>
            <Input value={target} onChange={(e) => setTarget(e.target.value)} placeholder="ex.: deepseek/deepseek-v4-flash:nitro" />
          </label>
        </div>

        {isOpenRouter && (
          <div className="flex flex-col gap-3 rounded-md border border-border p-3">
            <div className="flex items-center justify-between">
              <div className="flex flex-col">
                <span className="text-[13px] font-medium">Roteamento de provedor (OpenRouter)</span>
                <span className="text-[11px] text-muted-foreground">
                  Desligado = usa o pin global do env (hoje: order novita).
                </span>
              </div>
              <Switch checked={prefsEnabled} onCheckedChange={setPrefsEnabled} aria-label="ativar provider prefs" />
            </div>
            {prefsEnabled && <ProviderPrefsEditor value={prefs} onChange={setPrefs} />}
          </div>
        )}

        {error && (
          <p className="text-[13px] text-destructive" role="alert">
            {error}
          </p>
        )}

        <DialogFooter>
          <Button type="button" variant="outline" disabled={pending} onClick={onClose}>
            Cancelar
          </Button>
          <Button type="button" disabled={pending || !alias || !target} onClick={() => void save()}>
            {pending ? "Salvando…" : "Salvar"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
