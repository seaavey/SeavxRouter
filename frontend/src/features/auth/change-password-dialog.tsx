import { useState } from 'react'
import { useForm } from '@tanstack/react-form'
import { useMutation } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

type Values = { current_password: string; password: string; confirmation: string }

/** Rotates the dashboard password. The router replaces the session cookie on success. */
export function ChangePasswordDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [done, setDone] = useState(false)

  const change = useMutation({
    mutationFn: (value: Values) =>
      api('/dashboard/api/password', {
        method: 'POST',
        body: JSON.stringify({ current_password: value.current_password, password: value.password }),
      }),
  })

  const form = useForm({
    defaultValues: { current_password: '', password: '', confirmation: '' },
    onSubmit: ({ value }) => change.mutate(value, { onSuccess: () => setDone(true) }),
  })

  function close() {
    if (change.isPending) return
    setDone(false)
    change.reset()
    form.reset()
    onClose()
  }

  return (
    <Dialog open={open} onOpenChange={next => { if (!next) close() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{done ? 'Password changed' : 'Change password'}</DialogTitle>
          <DialogDescription>
            {done
              ? 'The new password is in effect and this session continues.'
              : 'Your current password is required. The new one needs at least 12 characters.'}
          </DialogDescription>
        </DialogHeader>

        {done ? (
          <DialogFooter className="-mx-4 -mb-4">
            <Button onClick={close}>Done</Button>
          </DialogFooter>
        ) : (
          <form
            onSubmit={event => {
              event.preventDefault()
              event.stopPropagation()
              void form.handleSubmit()
            }}
          >
            <FieldGroup className="gap-4">
              <form.Field
                name="current_password"
                validators={{ onChange: ({ value }) => (value ? undefined : 'Enter your current password.') }}
                children={field => (
                  <Field data-invalid={field.state.meta.errors.length > 0}>
                    <FieldLabel htmlFor="current-password">Current password</FieldLabel>
                    <Input
                      id="current-password"
                      type="password"
                      autoComplete="current-password"
                      value={field.state.value}
                      onChange={event => field.handleChange(event.target.value)}
                      onBlur={field.handleBlur}
                      autoFocus
                    />
                    {field.state.meta.isTouched && <FieldError>{String(field.state.meta.errors[0])}</FieldError>}
                  </Field>
                )}
              />

              <form.Field
                name="password"
                validators={{
                  onChange: ({ value }) => (value.length >= 12 ? undefined : 'Use at least 12 characters.'),
                }}
                children={field => (
                  <Field data-invalid={field.state.meta.errors.length > 0}>
                    <FieldLabel htmlFor="new-password">New password</FieldLabel>
                    <Input
                      id="new-password"
                      type="password"
                      autoComplete="new-password"
                      value={field.state.value}
                      onChange={event => field.handleChange(event.target.value)}
                      onBlur={field.handleBlur}
                    />
                    {field.state.meta.isTouched
                      ? <FieldError>{String(field.state.meta.errors[0] ?? '')}</FieldError>
                      : <FieldDescription>At least 12 characters.</FieldDescription>}
                  </Field>
                )}
              />

              <form.Field
                name="confirmation"
                validators={{
                  onChangeListenTo: ['password'],
                  onChange: ({ value, fieldApi }) =>
                    value === fieldApi.form.getFieldValue('password') ? undefined : 'The passwords do not match.',
                }}
                children={field => (
                  <Field data-invalid={field.state.meta.errors.length > 0}>
                    <FieldLabel htmlFor="confirm-password">Confirm new password</FieldLabel>
                    <Input
                      id="confirm-password"
                      type="password"
                      autoComplete="new-password"
                      value={field.state.value}
                      onChange={event => field.handleChange(event.target.value)}
                      onBlur={field.handleBlur}
                    />
                    {field.state.meta.isTouched && <FieldError>{String(field.state.meta.errors[0])}</FieldError>}
                  </Field>
                )}
              />

              {change.isError && <FieldError>{change.error.message}</FieldError>}

              <DialogFooter className="-mx-4 -mb-4">
                <Button type="button" variant="outline" onClick={close}>Cancel</Button>
                <form.Subscribe
                  selector={state => state.canSubmit && !change.isPending}
                  children={enabled => (
                    <Button type="submit" disabled={!enabled}>
                      {change.isPending && <Spinner />}
                      Save password
                    </Button>
                  )}
                />
              </DialogFooter>
            </FieldGroup>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
