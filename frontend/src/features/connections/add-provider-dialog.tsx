import { useState } from 'react'
import { useForm } from '@tanstack/react-form'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, ShieldCheck } from 'lucide-react'
import { api } from '@/lib/api'
import { keys } from '@/lib/queries'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { normalizeBaseURL, providerSDKs, validateAPIKey, validateBaseURL, validateProviderName, type ProviderSDK } from './provider-sdk'

const API_PATH = '/dashboard/api/connections'
const VALIDATE_PATH = '/dashboard/api/connections/validate'

type ProbeResult = { name: string; base_url: string; sdk: string; models: string[] }
type FormValues = { name: string; baseURL: string; sdk: ProviderSDK; apiKey: string }

/**
 * A verification only counts for the exact values it ran against, so editing
 * any field after a successful check disables Add again. Without this a key
 * validated for one endpoint could be stored against another.
 */
function verificationKey(values: FormValues) {
  return [values.name.trim().toLowerCase(), normalizeBaseURL(values.baseURL), values.sdk, values.apiKey.trim()].join('\u0000')
}

function requestBody(values: FormValues) {
  return JSON.stringify({
    name: values.name.trim().toLowerCase(),
    base_url: normalizeBaseURL(values.baseURL),
    sdk: values.sdk,
    api_key: values.apiKey.trim(),
  })
}

export function AddProviderDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [probe, setProbe] = useState<ProbeResult | null>(null)
  const [verified, setVerified] = useState<string | null>(null)

  const validate = useMutation({
    mutationFn: (values: FormValues) => api<ProbeResult>(VALIDATE_PATH, { method: 'POST', body: requestBody(values) }),
    onSuccess: (result, values) => {
      setProbe(result)
      setVerified(verificationKey(values))
    },
  })

  const add = useMutation({
    mutationFn: (values: FormValues) => api<ProbeResult>(API_PATH, { method: 'POST', body: requestBody(values) }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: keys.providers }),
        queryClient.invalidateQueries({ queryKey: keys.models }),
      ])
      close()
    },
  })

  const form = useForm({
    defaultValues: { name: '', baseURL: '', sdk: 'openai' as ProviderSDK, apiKey: '' },
    onSubmit: ({ value }) => {
      // The button is disabled until a matching verification, but Enter can
      // still submit the form, so the same gate is enforced here.
      if (verified !== verificationKey(value)) return
      add.mutate(value)
    },
  })

  function close() {
    if (add.isPending) return
    setProbe(null)
    setVerified(null)
    validate.reset()
    add.reset()
    form.reset()
    onClose()
  }

  const pending = add.isPending || validate.isPending
  const error = add.isError ? add.error.message : validate.isError ? validate.error.message : ''

  return (
    <Dialog open={open} onOpenChange={next => { if (!next) close() }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add provider</DialogTitle>
          <DialogDescription>
            The key is verified by fetching the provider's model catalog. Nothing is stored until that succeeds.
          </DialogDescription>
        </DialogHeader>

        <form
          onSubmit={event => {
            event.preventDefault()
            event.stopPropagation()
            void form.handleSubmit()
          }}
        >
          <FieldGroup className="gap-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <form.Field
                name="name"
                validators={{ onChange: ({ value }) => validateProviderName(value) }}
                children={field => (
                  <Field data-invalid={field.state.meta.errors.length > 0}>
                    <FieldLabel htmlFor="provider-name">Name</FieldLabel>
                    <Input
                      id="provider-name"
                      value={field.state.value}
                      onChange={event => field.handleChange(event.target.value)}
                      onBlur={field.handleBlur}
                      placeholder="myrouter"
                      autoComplete="off"
                      autoFocus
                    />
                    <FieldDescription>Prefixes model IDs as name/model.</FieldDescription>
                    {field.state.meta.isTouched && <FieldError errors={field.state.meta.errors.map(message => ({ message: String(message) }))} />}
                  </Field>
                )}
              />

              <form.Field
                name="sdk"
                children={field => (
                  <Field>
                    <FieldLabel htmlFor="provider-sdk">SDK</FieldLabel>
                    <Select value={field.state.value} onValueChange={value => field.handleChange(value as ProviderSDK)}>
                      <SelectTrigger id="provider-sdk" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {providerSDKs.map(entry => (
                          <SelectItem key={entry.value} value={entry.value}>
                            {entry.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FieldDescription>
                      {providerSDKs.find(entry => entry.value === field.state.value)?.hint}
                    </FieldDescription>
                  </Field>
                )}
              />
            </div>

            <form.Field
              name="baseURL"
              validators={{ onChange: ({ value }) => validateBaseURL(value) }}
              children={field => (
                <Field data-invalid={field.state.meta.errors.length > 0}>
                  <FieldLabel htmlFor="provider-base-url">Base URL</FieldLabel>
                  <Input
                    id="provider-base-url"
                    value={field.state.value}
                    onChange={event => field.handleChange(event.target.value)}
                    onBlur={field.handleBlur}
                    placeholder="https://api.example.com/v1"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  {field.state.meta.isTouched && <FieldError errors={field.state.meta.errors.map(message => ({ message: String(message) }))} />}
                </Field>
              )}
            />

            <form.Field
              name="apiKey"
              validators={{ onChange: ({ value }) => validateAPIKey(value) }}
              children={field => (
                <Field data-invalid={field.state.meta.errors.length > 0}>
                  <FieldLabel htmlFor="provider-api-key">API key</FieldLabel>
                  <Input
                    id="provider-api-key"
                    type="password"
                    value={field.state.value}
                    onChange={event => field.handleChange(event.target.value)}
                    onBlur={field.handleBlur}
                    placeholder="sk-…"
                    spellCheck={false}
                    autoComplete="off"
                  />
                  {field.state.meta.isTouched && <FieldError errors={field.state.meta.errors.map(message => ({ message: String(message) }))} />}
                </Field>
              )}
            />

            {error && <FieldError>{error}</FieldError>}

            <form.Subscribe
              selector={state => ({ canSubmit: state.canSubmit, values: state.values })}
              children={({ canSubmit, values }) => {
                const isVerified = verified !== null && verified === verificationKey(values)
                return (
                  <>
                    {isVerified && probe && (
                      <p role="status" className="flex items-center gap-2 rounded-lg bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
                        <Check className="size-4 shrink-0 text-positive" />
                        {probe.models.length} models reachable through {probe.name}
                      </p>
                    )}
                    {!isVerified && probe && !error && (
                      <p className="text-sm text-muted-foreground">Verify the key again to enable Add provider.</p>
                    )}
                    <DialogFooter>
                      <Button type="button" variant="outline" onClick={close} disabled={pending}>
                        Cancel
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        disabled={!canSubmit || pending}
                        onClick={() => {
                          add.reset()
                          validate.mutate(values)
                        }}
                      >
                        {validate.isPending ? <Spinner /> : <ShieldCheck />}
                        {isVerified ? 'Verify again' : 'Validate key'}
                      </Button>
                      <Button type="submit" disabled={!canSubmit || pending || !isVerified}>
                        {add.isPending && <Spinner />}
                        Add provider
                      </Button>
                    </DialogFooter>
                  </>
                )
              }}
            />
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
