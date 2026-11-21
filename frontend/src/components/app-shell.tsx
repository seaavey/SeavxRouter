import { Link, Outlet, useRouterState } from '@tanstack/react-router'
import { ChevronRight, KeyRound, LayoutDashboard, ListFilter, LogOut, Plug, Waypoints } from 'lucide-react'
import { useState } from 'react'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from '@/components/ui/sidebar'
import { Separator } from '@/components/ui/separator'
import { ThemeToggle } from '@/components/theme-toggle'
import { ChangePasswordDialog } from '@/features/auth/change-password-dialog'

const navigation = [
  { to: '/', label: 'Overview', icon: LayoutDashboard },
  { to: '/requests', label: 'Requests', icon: ListFilter },
  { to: '/connections', label: 'Connections', icon: Plug },
  { to: '/keys', label: 'API keys', icon: KeyRound },
] as const

/** Sidebar plus topbar: the frame every signed-in page renders inside. */
export function AppShell({ signingOut, onSignOut }: { signingOut: boolean; onSignOut: () => void }) {
  const pathname = useRouterState({ select: state => state.location.pathname })
  const current = navigation.find(item => (item.to === '/' ? pathname === '/' : pathname.startsWith(item.to)))
  const [passwordOpen, setPasswordOpen] = useState(false)

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader className="h-14 justify-center px-3">
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" asChild>
                <Link to="/" className="gap-2.5">
                  <span className="grid size-7 shrink-0 place-items-center rounded-md bg-foreground text-background">
                    <Waypoints className="size-4" />
                  </span>
                  <span className="grid leading-tight group-data-[collapsible=icon]:hidden">
                    <span className="text-sm font-medium">SeavxRouter</span>
                    <span className="text-xs text-muted-foreground">gateway</span>
                  </span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        <SidebarContent className="px-1 pt-1">
          <SidebarGroup className="p-0">
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {navigation.map(item => (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton
                      asChild
                      tooltip={item.label}
                      isActive={item.to === '/' ? pathname === '/' : pathname.startsWith(item.to)}
                      className="h-8 text-sm"
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

        <SidebarFooter className="gap-0.5 p-1">
          <Separator className="mb-1" />
          <SidebarMenu className="gap-0.5">
            <SidebarMenuItem>
              <SidebarMenuButton tooltip="Change password" onClick={() => setPasswordOpen(true)} className="h-8 text-sm">
                <KeyRound className="size-4" />
                <span>Change password</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem>
              <SidebarMenuButton tooltip="Sign out" disabled={signingOut} onClick={onSignOut} className="h-8 text-sm">
                <LogOut className="size-4" />
                <span>Sign out</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
      </Sidebar>

      <ChangePasswordDialog open={passwordOpen} onClose={() => setPasswordOpen(false)} />

      <SidebarInset>
        <header className="sticky top-0 z-10 flex h-14 shrink-0 items-center gap-2 border-b border-border bg-background px-4 md:px-8">
          <SidebarTrigger className="-ml-1 text-muted-foreground" />
          <Separator orientation="vertical" className="mr-1 h-4" />
          <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-sm">
            <span className="hidden text-muted-foreground sm:inline">Gateway</span>
            <ChevronRight className="hidden size-3.5 text-muted-foreground/60 sm:block" />
            <span className="truncate font-medium">{current?.label ?? 'Overview'}</span>
          </nav>
          <div className="ml-auto flex items-center gap-1">
            <ThemeToggle />
          </div>
        </header>

        <div className="mx-auto flex w-full max-w-[1180px] flex-1 flex-col px-4 py-8 md:px-8 md:py-10">
          <Outlet />
        </div>

        <footer className="mx-auto flex w-full max-w-[1180px] items-center justify-between gap-3 px-4 py-6 text-xs text-muted-foreground md:px-8">
          <span>SeavxRouter</span>
          <a href="https://github.com/seaavey/SeavxRouter" target="_blank" rel="noreferrer" className="hover:text-foreground">
            Documentation
          </a>
        </footer>
      </SidebarInset>
    </SidebarProvider>
  )
}
