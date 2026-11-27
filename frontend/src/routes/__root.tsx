import { Link, Outlet, createRootRouteWithContext } from '@tanstack/react-router'
import type { QueryClient } from '@tanstack/react-query'
import { Waypoints } from 'lucide-react'
import { ThemeProvider } from '@/components/theme-provider'
import { Button } from '@/components/ui/button'
import { Toaster } from '@/components/ui/sonner'
import { TooltipProvider } from '@/components/ui/tooltip'

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => (
    <ThemeProvider>
      <TooltipProvider delayDuration={300}>
        <Outlet />
        <Toaster position="bottom-right" />
      </TooltipProvider>
    </ThemeProvider>
  ),
  errorComponent: ({ error }) => (
    <RootProblem
      title="Something went wrong"
      message={error instanceof Error ? error.message : 'The dashboard could not load this page.'}
    />
  ),
  notFoundComponent: () => (
    <RootProblem
      title="Page not found"
      message="That address is not part of the dashboard."
    />
  ),
})

/** One calm screen for a missing page and for a page that could not load. */
function RootProblem({ title = 'Something went wrong', message }: {
  title?: string
  message?: string
}) {
  return (
    <main className="grid min-h-svh place-items-center px-6 py-12">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex items-center gap-2.5">
          <span className="grid size-8 place-items-center rounded-md bg-foreground text-background">
            <Waypoints className="size-4" />
          </span>
          <span className="grid leading-tight">
            <span className="text-sm font-medium">SeavxRouter</span>
            <span className="text-xs text-muted-foreground">Gateway dashboard</span>
          </span>
        </div>

        <h1 className="text-2xl leading-tight font-medium tracking-tight">{title}</h1>
        <p className="mt-1 mb-6 text-sm text-muted-foreground">
          {message ?? 'The dashboard could not load this page.'}
        </p>

        <div className="flex gap-2">
          <Button onClick={() => window.location.reload()}>Try again</Button>
          <Button variant="outline" asChild>
            <Link to="/">Go to overview</Link>
          </Button>
        </div>
      </div>
    </main>
  )
}
