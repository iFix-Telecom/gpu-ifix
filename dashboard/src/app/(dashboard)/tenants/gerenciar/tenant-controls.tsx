"use client";

/**
 * Client island for the tenant-management page (Phase 18, TEN-UI-10/11).
 *
 * The page (`page.tsx`) stays an RSC so the tenant list + owner-gate read run on
 * the server; the interactive affordances live here behind the COSMETIC owner
 * gate (mutation controls render only when `isOwner`). The authoritative re-check
 * is server-side in `@/lib/admin-actions` (`requireOwner` on every action — a
 * hidden-but-callable control is still gated, T-18-01).
 *
 * Surfaces:
 *   (1) TABELA de tenants — slug / name / created_at; rows expand → fetchTenantKeys
 *       on demand (skeleton while loading) → key_prefix / status / data_class badge.
 *   (2) CRIAR TENANT (owner) — dialog Slug + Nome → createTenant → refetch list.
 *   (3) GERAR KEY (owner, per tenant) — dialog with data-class select (normal|
 *       sensitive, default normal) → createTenantKey → raw key shown ONCE in a
 *       copyable block; the raw is NEVER persisted/refetched (T-18-04).
 *   (4) REVOGAR KEY (owner, per active key) — alert-dialog with an explicit IMPACT
 *       string, 1-click confirm (no text-entry step, default-focus Cancelar,
 *       --destructive confirm) → revokeKey → refetch keys.
 *
 * The data-class selector lives ONLY in the generate-key flow (data_class is
 * per-key, RES-08) — never in create-tenant. Copy is pt-BR.
 */
import { ChevronDown, ChevronRight, Copy, Plus, Trash2 } from "lucide-react";
import { Fragment, useState } from "react";
import { toast } from "sonner";
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
  Dialog,
  DialogClose,
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
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  createTenant,
  createTenantKey,
  revokeKey,
} from "@/lib/admin-actions";
import {
  fetchTenantKeys,
  fetchTenants,
  type TenantKeyRow,
  type TenantRow,
} from "@/lib/gateway";

const GENERIC_ERROR =
  "Não foi possível concluir a ação agora. Tente novamente em alguns segundos.";
/** Slug client-side check — mirrors the server (`/^[a-z0-9][a-z0-9-]*$/`). */
const SLUG_RE = /^[a-z0-9][a-z0-9-]*$/;

/** Generic 14×14 inline pending spinner (matches operator-controls idiom). */
function Spinner() {
  return (
    <span
      aria-hidden
      className="inline-block size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
    />
  );
}

/** Best-effort local date — falls back to the raw string on parse failure. */
function fmtDate(iso: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" });
}

export function TenantControls({
  isOwner,
  initialTenants,
}: {
  isOwner: boolean;
  initialTenants: TenantRow[];
}) {
  const [tenants, setTenants] = useState<TenantRow[]>(initialTenants);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [keysBySlug, setKeysBySlug] = useState<Record<string, TenantKeyRow[]>>(
    {},
  );
  const [loadingSlug, setLoadingSlug] = useState<string | null>(null);
  // Owner-only mutation targets (single dialog instance each).
  const [genForSlug, setGenForSlug] = useState<string | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<{
    keyId: string;
    keyPrefix: string;
    slug: string;
  } | null>(null);

  async function loadKeys(slug: string) {
    setLoadingSlug(slug);
    try {
      const keys = await fetchTenantKeys(slug);
      setKeysBySlug((m) => ({ ...m, [slug]: keys }));
    } catch (err) {
      toast.error((err as Error)?.message ?? GENERIC_ERROR);
    } finally {
      setLoadingSlug(null);
    }
  }

  function toggleRow(slug: string) {
    if (expanded === slug) {
      setExpanded(null);
      return;
    }
    setExpanded(slug);
    if (!keysBySlug[slug]) void loadKeys(slug);
  }

  async function refreshTenants() {
    try {
      setTenants(await fetchTenants());
    } catch (err) {
      toast.error((err as Error)?.message ?? GENERIC_ERROR);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {/* (2) CRIAR TENANT — owner only. */}
      {isOwner && (
        <div className="flex justify-end">
          <CreateTenantDialog onCreated={refreshTenants} />
        </div>
      )}

      <div className="rounded-md border border-border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" aria-label="expandir" />
              <TableHead>Slug</TableHead>
              <TableHead>Nome</TableHead>
              <TableHead>Criado</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tenants.length === 0 ? (
              <TableRow>
                <TableCell
                  colSpan={4}
                  className="text-center text-muted-foreground"
                >
                  Nenhum tenant cadastrado.
                </TableCell>
              </TableRow>
            ) : (
              tenants.map((t) => {
                const isOpen = expanded === t.slug;
                return (
                  <Fragment key={t.slug}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => toggleRow(t.slug)}
                    >
                      <TableCell>
                        {isOpen ? (
                          <ChevronDown className="size-4" />
                        ) : (
                          <ChevronRight className="size-4" />
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-[13px]">
                        {t.slug}
                      </TableCell>
                      <TableCell>{t.name}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {fmtDate(t.created_at)}
                      </TableCell>
                    </TableRow>
                    {isOpen && (
                      <TableRow className="bg-muted/30">
                        <TableCell colSpan={4} className="p-0">
                          <KeyPanel
                            slug={t.slug}
                            isOwner={isOwner}
                            loading={loadingSlug === t.slug}
                            keys={keysBySlug[t.slug]}
                            onGenerate={() => setGenForSlug(t.slug)}
                            onRevoke={(k) =>
                              setRevokeTarget({
                                keyId: k.id,
                                keyPrefix: k.key_prefix,
                                slug: t.slug,
                              })
                            }
                          />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>

      {/* (3) GERAR KEY — owner only, single dialog driven by genForSlug. */}
      {isOwner && (
        <GenerateKeyDialog
          slug={genForSlug}
          onOpenChange={(o) => !o && setGenForSlug(null)}
          onGenerated={(slug) => loadKeys(slug)}
        />
      )}

      {/* (4) REVOGAR KEY — owner only, single alert-dialog driven by revokeTarget. */}
      {isOwner && (
        <RevokeKeyDialog
          target={revokeTarget}
          onOpenChange={(o) => !o && setRevokeTarget(null)}
          onRevoked={(slug) => loadKeys(slug)}
        />
      )}
    </div>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// Keys sub-panel (read-only rows + owner-only generate/revoke affordances).
// ──────────────────────────────────────────────────────────────────────────

function KeyPanel({
  slug,
  isOwner,
  loading,
  keys,
  onGenerate,
  onRevoke,
}: {
  slug: string;
  isOwner: boolean;
  loading: boolean;
  keys: TenantKeyRow[] | undefined;
  onGenerate: () => void;
  onRevoke: (k: TenantKeyRow) => void;
}) {
  return (
    <div
      className="flex flex-col gap-3 px-4 py-3"
      onClick={(e) => e.stopPropagation()}
    >
      <div className="flex items-center justify-between">
        <span className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
          API keys — {slug}
        </span>
        {isOwner && (
          <Button type="button" size="sm" variant="outline" onClick={onGenerate}>
            <Plus className="size-4" />
            Gerar API key
          </Button>
        )}
      </div>

      {loading ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-6 w-full" />
          <Skeleton className="h-6 w-2/3" />
        </div>
      ) : !keys || keys.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">
          Nenhuma key para este tenant.
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Prefixo</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Classe</TableHead>
              <TableHead>Criada</TableHead>
              <TableHead>Último uso</TableHead>
              {isOwner && <TableHead aria-label="ações" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((k) => {
              const active = k.status === "active";
              return (
                <TableRow key={k.id}>
                  <TableCell className="font-mono text-[13px]">
                    {k.key_prefix}
                  </TableCell>
                  <TableCell>
                    <Badge variant={active ? "default" : "secondary"}>
                      {k.status}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Badge
                      variant={
                        k.data_class === "sensitive" ? "destructive" : "outline"
                      }
                    >
                      {k.data_class}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {fmtDate(k.created_at)}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {fmtDate(k.last_used_at)}
                  </TableCell>
                  {isOwner && (
                    <TableCell className="text-right">
                      {active && (
                        <button
                          type="button"
                          className="text-muted-foreground hover:text-destructive"
                          aria-label={`Revogar key ${k.key_prefix}`}
                          onClick={() => onRevoke(k)}
                        >
                          <Trash2 className="size-4" />
                        </button>
                      )}
                    </TableCell>
                  )}
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// (2) Criar tenant → dialog (Slug + Nome) → createTenant
// ──────────────────────────────────────────────────────────────────────────

function CreateTenantDialog({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = useState(false);
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  function reset() {
    setSlug("");
    setName("");
    setError(null);
    setPending(false);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!SLUG_RE.test(slug)) {
      setError(
        "Slug inválido: use minúsculas, dígitos e hífens (começando por letra ou dígito).",
      );
      return;
    }
    setPending(true);
    try {
      await createTenant({ slug, name });
      toast.success(`Tenant ${slug} criado.`);
      setOpen(false);
      reset();
      onCreated();
    } catch (err) {
      setError((err as Error)?.message ?? GENERIC_ERROR);
      setPending(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <Button type="button" onClick={() => setOpen(true)}>
        <Plus className="size-4" />
        Novo tenant
      </Button>
      <DialogContent className="gap-4" style={{ maxWidth: 384 }}>
        <DialogHeader>
          <DialogTitle>Novo tenant</DialogTitle>
          <DialogDescription>
            O slug identifica o tenant no gateway (minúsculas, dígitos e
            hífens). A classe de dados é definida por-key, ao gerar a key.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <label htmlFor="tenant-slug" className="text-xs font-semibold">
              Slug
            </label>
            <Input
              id="tenant-slug"
              type="text"
              autoComplete="off"
              placeholder="ex. novo-tenant"
              required
              value={slug}
              disabled={pending}
              onChange={(ev) => setSlug(ev.target.value)}
            />
          </div>
          <div className="flex flex-col gap-2">
            <label htmlFor="tenant-name" className="text-xs font-semibold">
              Nome
            </label>
            <Input
              id="tenant-name"
              type="text"
              autoComplete="off"
              required
              value={name}
              disabled={pending}
              onChange={(ev) => setName(ev.target.value)}
            />
          </div>
          {error && (
            <p className="text-xs text-destructive" role="alert">
              {error}
            </p>
          )}
          <DialogFooter className="gap-2">
            <DialogClose asChild>
              <Button type="button" variant="ghost" disabled={pending}>
                Cancelar
              </Button>
            </DialogClose>
            <Button type="submit" disabled={pending}>
              {pending ? (
                <span className="inline-flex items-center gap-2">
                  <Spinner />
                  Criando…
                </span>
              ) : (
                "Criar tenant"
              )}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// (3) Gerar key → dialog (data-class select) → createTenantKey → raw shown 1×
// ──────────────────────────────────────────────────────────────────────────

function GenerateKeyDialog({
  slug,
  onOpenChange,
  onGenerated,
}: {
  slug: string | null;
  onOpenChange: (open: boolean) => void;
  onGenerated: (slug: string) => void;
}) {
  const [dataClass, setDataClass] = useState("normal");
  const [rawKey, setRawKey] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function handleClose(open: boolean) {
    onOpenChange(open);
    if (!open) {
      // Raw key is discarded on close — never persisted (T-18-04).
      setRawKey(null);
      setDataClass("normal");
      setError(null);
      setPending(false);
    }
  }

  async function handleGenerate() {
    if (!slug) return;
    setError(null);
    setPending(true);
    try {
      const created = await createTenantKey({ slug, dataClass });
      setRawKey(created.key ?? null);
      onGenerated(slug);
    } catch (err) {
      setError((err as Error)?.message ?? GENERIC_ERROR);
    } finally {
      setPending(false);
    }
  }

  async function copyRaw() {
    if (!rawKey) return;
    try {
      await navigator.clipboard.writeText(rawKey);
      toast.success("Key copiada.");
    } catch {
      toast.error("Não foi possível copiar automaticamente. Copie manualmente.");
    }
  }

  return (
    <Dialog open={slug !== null} onOpenChange={handleClose}>
      <DialogContent className="gap-4" style={{ maxWidth: 448 }}>
        <DialogHeader>
          <DialogTitle>Gerar API key {slug ? `— ${slug}` : ""}</DialogTitle>
          <DialogDescription>
            A classe de dados vale para esta key. Keys <code>sensitive</code>{" "}
            nunca roteiam para providers externos (RES-08).
          </DialogDescription>
        </DialogHeader>

        {rawKey ? (
          <div className="flex flex-col gap-2">
            <p className="text-xs font-semibold text-destructive" role="alert">
              Copie agora — não será exibida de novo.
            </p>
            <div className="flex items-center gap-2">
              <Input
                readOnly
                value={rawKey}
                className="font-mono text-[13px]"
                onFocus={(e) => e.currentTarget.select()}
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label="Copiar key"
                onClick={copyRaw}
              >
                <Copy className="size-4" />
              </Button>
            </div>
            <DialogFooter>
              <DialogClose asChild>
                <Button type="button">Fechar</Button>
              </DialogClose>
            </DialogFooter>
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label htmlFor="key-data-class" className="text-xs font-semibold">
                Classe de dados
              </label>
              <Select
                value={dataClass}
                onValueChange={setDataClass}
                disabled={pending}
              >
                <SelectTrigger id="key-data-class">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="normal">normal</SelectItem>
                  <SelectItem value="sensitive">sensitive</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {error && (
              <p className="text-xs text-destructive" role="alert">
                {error}
              </p>
            )}
            <DialogFooter className="gap-2">
              <DialogClose asChild>
                <Button type="button" variant="ghost" disabled={pending}>
                  Cancelar
                </Button>
              </DialogClose>
              <Button type="button" disabled={pending} onClick={handleGenerate}>
                {pending ? (
                  <span className="inline-flex items-center gap-2">
                    <Spinner />
                    Gerando…
                  </span>
                ) : (
                  "Gerar key"
                )}
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// (4) Revogar key → alert-dialog com string de impacto → revokeKey
// ──────────────────────────────────────────────────────────────────────────

function RevokeKeyDialog({
  target,
  onOpenChange,
  onRevoked,
}: {
  target: { keyId: string; keyPrefix: string; slug: string } | null;
  onOpenChange: (open: boolean) => void;
  onRevoked: (slug: string) => void;
}) {
  const [pending, setPending] = useState(false);

  async function handleConfirm() {
    if (!target) return;
    setPending(true);
    try {
      await revokeKey({ keyId: target.keyId });
      toast.success(`Key ${target.keyPrefix} revogada.`);
      onRevoked(target.slug);
      onOpenChange(false);
    } catch (err) {
      toast.error((err as Error)?.message ?? GENERIC_ERROR);
    } finally {
      setPending(false);
    }
  }

  return (
    <AlertDialog open={target !== null} onOpenChange={onOpenChange}>
      <AlertDialogContent style={{ maxWidth: 448 }}>
        <AlertDialogHeader>
          <AlertDialogTitle>Revogar API key?</AlertDialogTitle>
          <AlertDialogDescription>
            {target
              ? `Revogar a key ${target.keyPrefix} do tenant ${target.slug}? Qualquer app usando essa key passa a receber 401 imediatamente e para de funcionar. Esta ação não pode ser desfeita.`
              : ""}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter className="gap-2">
          <AlertDialogCancel disabled={pending}>Cancelar</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={(e) => {
              e.preventDefault();
              handleConfirm();
            }}
          >
            {pending ? (
              <span className="inline-flex items-center gap-2">
                <Spinner />
                Revogar key
              </span>
            ) : (
              "Revogar key"
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
