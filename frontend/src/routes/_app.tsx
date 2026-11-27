import { createFileRoute, redirect } from '@tanstack/react-router'
import { AppShell } from '@/components/app-shell'
import { api } from '@/lib/api'
import { sessionQuery } from '@/lib/queries'

/**
 * The signed-in frame. `beforeLoad` runs first so no protected page ever
 * paints for an unauthenticated visitor.
 */
export const Route = createFileRoute('/_app')({
  beforeLoad: async ({ context, location }) => {
    const session = await context.queryClient.ensureQueryData(sessionQuery)
    if (!session.authenticated || session.must_change) {
      throw redirect({ to: '/sign-in', search: { redirect: location.href } })
    }
  },
  component: AppLayout,
})

function AppLayout() {
  return (
    <AppShell
      signingOut={false}
      onSignOut={() => {
        void api('/dashboard/api/logout', { method: 'POST' }).finally(() => {
          window.location.assign('/sign-in')
        })
      }}
    />
  )
}
