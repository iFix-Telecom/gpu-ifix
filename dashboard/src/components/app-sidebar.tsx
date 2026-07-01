"use client";

/**
 * Left navigation for the (dashboard) layout — three views: Overview,
 * Tenants, Incident History. Built on the shadcn `sidebar` block.
 *
 * UI-SPEC §Color — the accent (`--primary`) is reserved for, among other
 * things, "the active sidebar nav item"; the active route uses
 * `data-active` which the sidebar block paints with the sidebar accent.
 */

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useState } from "react";
import {
  Activity,
  LogOut,
  Receipt,
  ScrollText,
  ServerCog,
  Settings,
  SlidersHorizontal,
  TrendingUp,
  UserCog,
  Users,
} from "lucide-react";

import { signOut, useSession } from "@/lib/auth-client";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";

const NAV_ITEMS = [
  { href: "/", label: "Visão geral", icon: Activity },
  { href: "/tenants", label: "Tenants", icon: Users },
  { href: "/consumo", label: "Consumo", icon: Receipt },
  { href: "/operacao", label: "Operação", icon: ServerCog },
  { href: "/operacao/config", label: "Config do pod", icon: SlidersHorizontal },
  { href: "/economia", label: "Economia", icon: TrendingUp },
  { href: "/incidents", label: "Histórico de incidentes", icon: ScrollText },
] as const;

export function AppSidebar() {
  const pathname = usePathname();
  const router = useRouter();
  const { data: session } = useSession();
  const [signingOut, setSigningOut] = useState(false);

  const user = session?.user as
    | { name?: string; email?: string; role?: string }
    | undefined;
  const isOwner = user?.role === "owner";

  async function handleSignOut() {
    setSigningOut(true);
    try {
      await signOut();
    } finally {
      router.push("/signed-out");
    }
  }

  // Pick the single most-specific nav match (longest href that prefixes the
  // current path) so `/operacao/config` lights up "Config do pod" only — not
  // also its parent "Operação".
  const activeHref = NAV_ITEMS.reduce<string | null>((best, item) => {
    const matches =
      item.href === "/"
        ? pathname === "/"
        : pathname === item.href || pathname.startsWith(`${item.href}/`);
    if (!matches) return best;
    if (best === null || item.href.length > best.length) return item.href;
    return best;
  }, null);

  return (
    <Sidebar>
      <SidebarHeader>
        {/* Heading 20/600 per the UI-SPEC typography table. */}
        <div className="flex items-center gap-2 px-2 py-1">
          <Activity className="size-5 text-primary" />
          <span className="text-[20px] font-semibold leading-tight">
            Gateway ifix-ai
          </span>
        </div>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupLabel>Observabilidade</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {NAV_ITEMS.map((item) => {
                const isActive = item.href === activeHref;
                return (
                  <SidebarMenuItem key={item.href}>
                    <SidebarMenuButton
                      asChild
                      isActive={isActive}
                      tooltip={item.label}
                    >
                      <Link href={item.href}>
                        <item.icon />
                        <span>{item.label}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                );
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
      <SidebarFooter>
        <SidebarGroup>
          <SidebarGroupLabel>Conta</SidebarGroupLabel>
          <SidebarGroupContent>
            {/* Logged-in identity (name/email + role) — read-only. */}
            {user && (
              <div className="px-2 py-1.5 text-xs leading-tight">
                <div className="truncate font-medium">
                  {user.name ?? user.email ?? "Usuário"}
                </div>
                {user.email && user.name && (
                  <div className="truncate text-muted-foreground">
                    {user.email}
                  </div>
                )}
                <div className="mt-0.5 text-muted-foreground">
                  {isOwner ? "owner" : "operator"}
                </div>
              </div>
            )}
            <SidebarMenu>
              <SidebarMenuItem>
                <SidebarMenuButton
                  asChild
                  isActive={pathname === "/settings"}
                  tooltip="Configurações"
                >
                  <Link href="/settings">
                    <Settings />
                    <span>Configurações</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
              {/* Operator management is owner-only (Phase 13 D-02) — the page
                  itself is server-side owner-gated; hide the link for operators. */}
              {isOwner && (
                <SidebarMenuItem>
                  <SidebarMenuButton
                    asChild
                    isActive={pathname.startsWith("/settings/operadores")}
                    tooltip="Operadores"
                  >
                    <Link href="/settings/operadores">
                      <UserCog />
                      <span>Operadores</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              )}
              <SidebarMenuItem>
                <SidebarMenuButton
                  onClick={handleSignOut}
                  disabled={signingOut}
                  tooltip="Sair"
                >
                  <LogOut />
                  <span>{signingOut ? "Saindo…" : "Sair"}</span>
                </SidebarMenuButton>
              </SidebarMenuItem>
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarFooter>
    </Sidebar>
  );
}
