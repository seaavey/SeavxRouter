import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { FieldError } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'

/** Confirmation for a destructive action: revoking is immediate and irreversible. */
export function RevokeKeyDialog({ keyName, pending, error, onConfirm, onClose }: {
  keyName?: string
  pending: boolean
  error?: string
  onConfirm: () => void
  onClose: () => void
}) {
  return (
    <Dialog open={Boolean(keyName)} onOpenChange={next => { if (!next && !pending) onClose() }}>
      <DialogContent className="sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>Revoke {keyName}</DialogTitle>
          <DialogDescription>Requests using this key lose access immediately. This cannot be undone.</DialogDescription>
        </DialogHeader>
        {error && <FieldError>{error}</FieldError>}
        <DialogFooter className="-mx-4 -mb-4">
          <Button variant="outline" disabled={pending} onClick={onClose}>Cancel</Button>
          <Button variant="destructive" disabled={pending} onClick={onConfirm}>
            {pending && <Spinner />}
            Revoke key
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
