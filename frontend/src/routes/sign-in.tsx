import { createFileRoute, redirect } from '@tanstack/react-router'
import { SignInPage } from '@/features/auth/sign-in'
import { sessionQuery } from '@/lib/queries'

export const Route = createFileRoute('/sign-in')({
  validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof search.redirect === 'string' ? search.redirect : undefined,
  }),
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(sessionQuery)
    if (session.authenticated && !session.must_change) throw redirect({ to: '/' })
  },
  component: SignInPage,
})
