import { useEffect, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Waypoints } from 'lucide-react'
import { api, type Session } from '@/lib/api'
import { keys, sessionQuery } from '@/lib/queries'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

/**
 * Two steps on one page, because the router decides which one applies:
 * sign in (with the setup token on a fresh install), then set a real password.
 */
export function SignInPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const session = useQuery(sessionQuery)
  const current = session.data
  const replacing = Boolean(current?.authenticated && current.must_change)
  const needsToken = Boolean(current?.setup_required || current?.must_change)

  const signIn = useMutation({
    mutationFn: (body: { password: string; setup_token?: string }) =>
      api<Session>(`/dashboard/api/${replacing ? 'password' : 'login'}`, { method: 'POST', body: JSON.stringify(body) }),
    onSuccess: next => {
      queryClient.setQueryData(keys.session, next)
      if (next.authenticated && !next.must_change) void navigate({ to: '/' })
    },
  })

  useEffect(() => {
    const expire = () => queryClient.invalidateQueries({ queryKey: keys.session })
    window.addEventListener('dashboard-session-expired', expire)
    return () => window.removeEventListener('dashboard-session-expired', expire)
  }, [queryClient])

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    signIn.mutate({
      password: String(form.get('password') ?? ''),
      setup_token: String(form.get('setup_token') ?? '') || undefined,
    })
  }

  if (session.isPending) {
    return <main className="grid min-h-svh place-items-center"><Spinner className="text-muted-foreground" /></main>
  }

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

        <h1 className="font-heading text-2xl leading-tight font-medium tracking-tight">
          {replacing ? 'Set a new password' : 'Sign in'}
        </h1>
        <p className="mt-1 mb-6 text-sm text-muted-foreground">
          {replacing
            ? 'The setup password only works once. Choose the password you keep.'
            : 'Use your dashboard password to continue.'}
        </p>

        {session.isError && (
          <p role="alert" className="mb-4 rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {session.error.message}
          </p>
        )}

        <form onSubmit={submit} key={replacing ? 'password' : 'login'}>
          <FieldGroup className="gap-4">
            <Field>
              <FieldLabel htmlFor="password">{replacing ? 'New password' : 'Password'}</FieldLabel>
              <Input
                id="password"
                name="password"
                type="password"
                required
                minLength={replacing ? 12 : undefined}
                autoComplete={replacing ? 'new-password' : 'current-password'}
                autoFocus
              />
              {replacing && <FieldDescription>At least 12 characters.</FieldDescription>}
            </Field>

            {needsToken && (
              <Field>
                <FieldLabel htmlFor="setup_token">Setup token</FieldLabel>
                <Input
                  id="setup_token"
                  name="setup_token"
                  type="password"
                  required
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono"
                />
                <FieldDescription>
                  The router writes it beside the dashboard database as `dashboard.db.setup-token`.
                </FieldDescription>
              </Field>
            )}

            {signIn.isError && <FieldError>{signIn.error.message}</FieldError>}

            <Button type="submit" size="lg" className="w-full" disabled={signIn.isPending}>
              {signIn.isPending && <Spinner />}
              {replacing ? 'Save password' : 'Sign in'}
            </Button>
          </FieldGroup>
        </form>
      </div>
    </main>
  )
}
