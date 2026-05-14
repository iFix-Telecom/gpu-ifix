"use client";

/**
 * Tenants — the per-tenant metrics table plus a date-range cost filter.
 *
 * The table polls `fetchMetrics` (the provider's 5–10s refetchInterval). The
 * cost panel is a `select` (tenant) + `popover`+`calendar` (from/to range)
 * whose "Aplicar período" button (UI-SPEC §Copywriting) drives a
 * `fetchUsage` query — enabled only once a tenant + range is applied.
 *
 * Loading → `skeleton`; fetch failure → the pt-BR error state with a
 * "Tentar novamente" button (UI-SPEC §Copywriting).
 */

import { useQuery } from "@tanstack/react-query";
import { CalendarIcon } from "lucide-react";
import { useState } from "react";
import type { DateRange } from "react-day-picker";

import { StaleIndicator } from "@/components/stale-indicator";
import { TenantTable } from "@/components/tenant-table";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Calendar } from "@/components/ui/calendar";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBrl } from "@/lib/format";
import {
  fetchMetrics,
  fetchUsage,
  GatewayError,
  tenantLabel,
} from "@/lib/gateway";

/** WR-06: the specific proxy/gateway cause, or the generic fallback. */
function errorMessage(error: unknown): string {
  return error instanceof GatewayError
    ? error.message
    : "Não foi possível carregar as métricas do gateway.";
}

/**
 * YYYY-MM-DD — the `/admin/usage` from/to query format.
 *
 * WR-08: format the LOCAL date components directly. `react-day-picker`
 * returns `Date` objects at local midnight; round-tripping through
 * `toISOString()` shifts the operator's selected calendar day by their
 * UTC offset (e.g. a positive-offset timezone gets the previous day),
 * producing wrong cost numbers for the boundary days. The gateway
 * interprets from/to in America/Sao_Paulo, so the date string must be
 * exactly the calendar day the operator picked.
 */
function isoDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export default function TenantsPage() {
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: () => fetchMetrics(),
  });

  // Date-range filter state — `applied` holds the confirmed tenant+range that
  // actually drives the usage query (only set on "Aplicar período").
  const [selectedTenant, setSelectedTenant] = useState<string>("");
  const [range, setRange] = useState<DateRange | undefined>();
  const [applied, setApplied] = useState<{
    tenant: string;
    from: string;
    to: string;
  } | null>(null);

  const usageQuery = useQuery({
    queryKey: ["usage", applied],
    queryFn: () =>
      fetchUsage(applied!.tenant, applied!.from, applied!.to),
    enabled: applied !== null,
  });

  // De-duplicate tenants by UUID, keeping the human label for display.
  // The Select VALUE stays the UUID (the stable id `/admin/usage` accepts),
  // but the operator sees the name/slug, not a bare UUID (WR-10).
  const tenantOptions = Array.from(
    new Map(
      (metricsQuery.data?.tenants ?? []).map((t) => [
        t.tenant_id,
        { id: t.tenant_id, label: tenantLabel(t) },
      ]),
    ).values(),
  );

  const canApply =
    selectedTenant !== "" && range?.from !== undefined && range?.to !== undefined;

  function applyPeriod() {
    if (!canApply || !range?.from || !range?.to) return;
    setApplied({
      tenant: selectedTenant,
      from: isoDate(range.from),
      to: isoDate(range.to),
    });
  }

  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-semibold leading-[1.2]">Tenants</h1>
        <StaleIndicator updatedAt={metricsQuery.dataUpdatedAt} />
      </div>

      {/* Per-tenant metrics table. */}
      <Card>
        <CardHeader>
          <CardTitle className="text-[20px] font-semibold">
            Métricas por tenant
          </CardTitle>
        </CardHeader>
        <CardContent>
          {metricsQuery.isLoading ? (
            <Skeleton className="h-48 w-full" />
          ) : metricsQuery.isError ? (
            <div className="flex flex-col items-center gap-4 py-8 text-center">
              <p className="text-[14px] text-muted-foreground">
                {errorMessage(metricsQuery.error)} Verifique se o gateway está
                no ar e se a admin-key está válida, depois use Tentar
                novamente.
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => metricsQuery.refetch()}
              >
                Tentar novamente
              </Button>
            </div>
          ) : (
            <TenantTable rows={metricsQuery.data?.tenants ?? []} />
          )}
        </CardContent>
      </Card>

      {/* Date-range cost filter. */}
      <Card>
        <CardHeader>
          <CardTitle className="text-[20px] font-semibold">
            Custo por período
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-2">
            <Select value={selectedTenant} onValueChange={setSelectedTenant}>
              <SelectTrigger className="w-56">
                <SelectValue placeholder="Selecione um tenant" />
              </SelectTrigger>
              <SelectContent>
                {tenantOptions.map((opt) => (
                  <SelectItem key={opt.id} value={opt.id}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            <Popover>
              <PopoverTrigger asChild>
                <Button size="sm" variant="outline">
                  <CalendarIcon />
                  {range?.from && range?.to
                    ? `${isoDate(range.from)} → ${isoDate(range.to)}`
                    : "Selecione o período"}
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-auto p-0" align="start">
                <Calendar
                  mode="range"
                  selected={range}
                  onSelect={setRange}
                  numberOfMonths={2}
                />
              </PopoverContent>
            </Popover>

            <Button size="sm" onClick={applyPeriod} disabled={!canApply}>
              Aplicar período
            </Button>
          </div>

          {applied === null ? (
            <p className="py-4 text-[14px] text-muted-foreground">
              Selecione um tenant e um período, depois use Aplicar período.
            </p>
          ) : usageQuery.isLoading ? (
            <Skeleton className="h-24 w-full" />
          ) : usageQuery.isError ? (
            <div className="flex flex-col items-center gap-4 py-4 text-center">
              <p className="text-[14px] text-muted-foreground">
                {errorMessage(usageQuery.error)} Verifique se o gateway está no
                ar e se a admin-key está válida, depois use Tentar novamente.
              </p>
              <Button
                size="sm"
                variant="outline"
                onClick={() => usageQuery.refetch()}
              >
                Tentar novamente
              </Button>
            </div>
          ) : usageQuery.data ? (
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <CostCell
                label="Custo total"
                value={formatBrl(usageQuery.data.summary.cost_total_brl)}
              />
              <CostCell
                label="Custo local"
                value={formatBrl(usageQuery.data.summary.cost_local_brl)}
              />
              <CostCell
                label="Custo externo"
                value={formatBrl(usageQuery.data.summary.cost_external_brl)}
              />
              <CostCell
                label="Requests"
                value={usageQuery.data.summary.requests_count.toLocaleString(
                  "pt-BR",
                )}
              />
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}

/** One cost figure — 12/600 label + tabular-nums value. */
function CostCell({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-[12px] font-semibold text-muted-foreground">
        {label}
      </span>
      <span className="text-[20px] font-semibold tabular-nums">{value}</span>
    </div>
  );
}
