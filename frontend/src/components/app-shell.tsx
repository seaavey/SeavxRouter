import { Link, Outlet, useRouterState } from '@tanstack/react-router'
import { useIsFetching, useQuery, useQueryClient } from '@tanstack/react-query'
import { Boxes, ChevronRight, KeyRound, LayoutDashboard, ListFilter, LogOut, RefreshCw, Waypoints } from 'lucide-react'
import { useState } from 'react'
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { Separator } from '@/components/ui/separator'
import { Button } from '@/components/ui/button'
import { StatusLabel } from '@/components/status'
import { ChangePasswordDialog } from '@/features/auth/change-password-dialog'
import { healthQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'

const navigation = [
  { to: '/', label: 'Overview', icon: LayoutDashboard },
  { to: '/requests', label: 'Requests', icon: ListFilter },
  { to: '/providers', label: 'Providers', icon: Boxes },
  { to: '/models', label: 'Models', icon: Waypoints },
  { to: '/keys', label: 'API keys', icon: KeyRound },
] as const

/** Sidebar plus topbar. Every signed-in route renders its content in the outlet. */
export function AppShell({ signingOut, onSignOut }: { signingOut: boolean; onSignOut: () => void }) {
  const pathname = useRouterState({ select: state => state.location.pathname })
  const queryClient = useQueryClient()
  const current = navigation.find(item => (item.to === '/' ? pathname === '/' : pathname.startsWith(item.to)))
  const refreshing = useIsFetching() > 0
  const [passwordOpen, setPasswordOpen] = useState(false)

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader className="h-14 justify-center px-2">
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" asChild>
                <Link to="/" className="gap-2.5">
                  <span className="grid size-7 shrink-0 place-items-center rounded-md bg-signal text-signal-foreground">
                    <Waypoints className="size-4" />
                  </span>
                  <span className="grid leading-tight group-data-[collapsible=icon]:hidden">
                    <span className="text-sm font-medium">SeavxRouter</span>
                    <span className="font-mono text-[11px] text-muted-foreground">gateway</span>
                  </span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>
        <Separator className="mx-2 group-data-[collapsible=icon]:mx-1" />
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupLabel className="font-mono text-[11px] tracking-normal text-muted-foreground/80">Monitoring</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {navigation.map(item => (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton
                      asChild
                      tooltip={item.label}
                      isActive={item.to === '/' ? pathname === '/' : pathname.startsWith(item.to)}
                    >
                      <Link to={item.to}>
                        <item.icon className="size-4" />
                        <span>{item.label}</span>
                      </Link>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
        <SidebarFooter className="border-t border-sidebar-border p-2">
          <SidebarMenu className="gap-0.5">
            <SidebarMenuItem>
              <SidebarMenuButton tooltip="Change password" onClick={() => setPasswordOpen(true)}>
                <KeyRound className="size-4" />
                <span>Change password</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <SidebarMenuButton tooltip="Sign out" disabled={signingOut} onClick={onSignOut}>
                <LogOut className="size-4" />
                <span>Sign out</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
      </Sidebar>

      <ChangePasswordDialog open={passwordOpen} onClose={() => setPasswordOpen(false)} />

      <SidebarInset>
        <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background/85 px-4 backdrop-blur md:px-8">
          <SidebarTrigger className="-ml-1 text-muted-foreground" />
          <Separator orientation="vertical" className="mr-1 h-4" />
          <nav aria-label="Breadcrumb" className="flex items-center gap-1.5 text-sm">
            <span className="text-muted-foreground">Workspace</span>
            <ChevronRight className="size-3.5 text-muted-foreground/60" />
            <span className="font-medium">{current?.label ?? 'Overview'}</span>
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <RouterStatus />
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Refresh data"
              onClick={() => queryClient.invalidateQueries()}
            >
              <RefreshCw className={cn('size-4', refreshing && 'animate-spin')} />
            </Button>
          </div>
        </header>

        <div className="mx-auto w-full max-w-[1400px] px-4 py-6 md:px-8 md:py-8">
          <Outlet />
        </div>

        <footer className="mx-auto flex w-full max-w-[1400px] items-center justify-between gap-3 border-t border-border px-4 py-4 text-xs text-muted-foreground md:px-8">
          <span className="font-mono">SeavxRouter</span>
          <a
            href="https://github.com/seaavey/SeavxRouter"
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 hover:text-foreground"
          >
            Source on GitHub
          </a>
        </footer>
      </SidebarInset>
    </SidebarProvider>
  )
}

/** The signature detail: one dot that says whether the gateway answers right now. */
function RouterStatus() {
  const health = useQuery(healthQuery)
  const tone = health.isError ? 'destructive' : health.isSuccess ? 'positive' : 'muted'
  const label = health.isError ? 'Gateway unreachable' : health.isSuccess ? 'Gateway online' : 'Checking gateway'
  return (
    <>
      <StatusLabel tone={tone} pulse={health.isSuccess}>
        <span className="hidden sm:inline">{label}</span>
        <span className="sr-only sm:hidden">{label}</span>
      </StatusLabel>
    </>
  )
}
