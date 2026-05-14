/**
 * (dashboard) route-group layout — wraps every authenticated view in the
 * React Query provider, the left sidebar, and the sticky critical banner.
 *
 * Layout: the banner sits ABOVE the page content (sticky top); the page
 * content region carries a `2xl`/48px top padding per the UI-SPEC spacing
 * scale ("Page top padding below the global banner/header").
 */

import { AppSidebar } from "@/components/app-sidebar";
import { CriticalBanner } from "@/components/critical-banner";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { QueryProvider } from "@/lib/query-client";

export default function DashboardLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <QueryProvider>
      {/* SidebarMenuButton renders collapsed-state tooltips — they need a
          TooltipProvider in scope (the shadcn SidebarProvider does not add
          one). */}
      <TooltipProvider delayDuration={0}>
        <SidebarProvider>
          <AppSidebar />
          <SidebarInset>
            {/* Sticky critical/warning banner — above the page content. */}
            <CriticalBanner />
            {/* Page content region: 2xl/48px top padding, lg/24px sides. */}
            <div className="flex flex-1 flex-col gap-8 px-6 pt-12 pb-8">
              {children}
            </div>
          </SidebarInset>
          <Toaster />
        </SidebarProvider>
      </TooltipProvider>
    </QueryProvider>
  );
}
