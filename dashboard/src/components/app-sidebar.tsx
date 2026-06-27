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
import { usePathname } from "next/navigation";
import {
  Activity,
  Receipt,
  ScrollText,
  ServerCog,
  TrendingUp,
  Users,
} from "lucide-react";

import {
  Sidebar,
  SidebarContent,
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
  { href: "/economia", label: "Economia", icon: TrendingUp },
  { href: "/incidents", label: "Histórico de incidentes", icon: ScrollText },
] as const;

export function AppSidebar() {
  const pathname = usePathname();

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
                const isActive =
                  item.href === "/"
                    ? pathname === "/"
                    : pathname.startsWith(item.href);
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
    </Sidebar>
  );
}
