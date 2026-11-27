import { useState } from 'react'
import { useForm } from '@tanstack/react-form'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Copy } from 'lucide-react'
import { api, type CreatedKey } from '@/lib/api'
import { keys } from '@/lib/queries'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

export function CreateKeyDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [created, setCreated] = useState('')
  const [copied, setCopied] = useState(false)

  const create = useMutation({
    mutationFn: (value: { name: string; budget: number; tokens: number }) =>
      api<CreatedKey>('/dashboard/api/keys', {
        method: 'POST',
        body: JSON.stringify({ name: value.name, budget_usd: value.budget, limit_token: value.tokens }),
      }),
    onSuccess: key => {
      setCreated(key.key)
      void queryClient.invalidateQueries({ queryKey: keys.tenantKeys })
    },
  })

  const form = useForm({
    defaultValues: { name: '', budget: 0, tokens: 0 },
    onSubmit: ({ value }) => {
      create.mutate(
        { name: value.name.trim(), budget: value.budget, tokens: value.tokens },
        { onSuccess: () => form.reset() },
      )
    },
  })

  function close() {
    if (create.isPending) return
    setCreated('')
    setCopied(false)
    create.reset()
    onClose()
  }

  async function copy() {
    await navigator.clipboard.writeText(created)
    setCopied(true)
  }

  return (
    <Dialog open={open} onOpenChange={next => { if (!next) close() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{created ? 'Key created' : 'Create tenant key'}</DialogTitle>
          <DialogDescription>
            {created
              ? 'Copy this key now. The router stores only its hash and never shows it again.'
              : 'Limits apply per calendar month. Leave zero for unlimited.'}
          </DialogDescription>
        </DialogHeader>

        {created ? (
          <div className="grid gap-4">
            <div className="flex items-center gap-2 rounded-md bg-muted/50 p-2 pl-3">
              <code className="min-w-0 flex-1 truncate font-mono text-xs">{created}</code>
              <Button variant="ghost" size="sm" onClick={copy} aria-label="Copy key">
                {copied ? <Check className="size-3.5 text-positive" /> : <Copy className="size-3.5" />}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            </div>
            <div className="flex justify-end">
              <Button onClick={close}>Done</Button>
            </div>
          </div>
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
                name="name"
                validators={{ onChange: ({ value }) => (value.trim() ? undefined : 'Give the key a name.') }}
                children={field => (
                  <Field data-invalid={field.state.meta.errors.length > 0}>
                    <FieldLabel htmlFor="key-name">Name</FieldLabel>
                    <Input
                      id="key-name"
                      value={field.state.value}
                      onChange={event => field.handleChange(event.target.value)}
                      onBlur={field.handleBlur}
                      placeholder="Production"
                      autoFocus
                    />
                    {field.state.meta.isTouched && <FieldError>{String(field.state.meta.errors[0])}</FieldError>}
                  </Field>
                )}
              />

              <div className="grid gap-4 sm:grid-cols-2">
                <form.Field
                  name="budget"
                  children={field => (
                    <Field>
                      <FieldLabel htmlFor="key-budget">Monthly budget</FieldLabel>
                      <Input
                        id="key-budget"
                        type="number"
                        min={0}
                        step="0.01"
                        value={field.state.value}
                        onChange={event => field.handleChange(Number(event.target.value))}
                        onBlur={field.handleBlur}
                      />
                      <FieldDescription>USD, zero for unlimited.</FieldDescription>
                    </Field>
                  )}
                />
                <form.Field
                  name="tokens"
                  children={field => (
                    <Field>
                      <FieldLabel htmlFor="key-tokens">Token limit</FieldLabel>
                      <Input
                        id="key-tokens"
                        type="number"
                        min={0}
                        step="1"
                        value={field.state.value}
                        onChange={event => field.handleChange(Number(event.target.value))}
                        onBlur={field.handleBlur}
                      />
                      <FieldDescription>Zero for unlimited.</FieldDescription>
                    </Field>
                  )}
                />
              </div>

              {create.isError && <FieldError>{create.error.message}</FieldError>}

              <DialogFooter className="-mx-4 -mb-4">
                <Button type="button" variant="outline" onClick={close}>Cancel</Button>
                <form.Subscribe
                  selector={state => state.canSubmit && !create.isPending}
                  children={enabled => (
                    <Button type="submit" disabled={!enabled}>
                      {create.isPending && <Spinner />}
                      Create key
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
