import { cva, type VariantProps } from 'class-variance-authority'
import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'
import type { ComponentProps } from 'react'

const buttonVariants = cva(
  'inline-flex h-9 shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 disabled:pointer-events-none disabled:opacity-50',
  {
    variants: {
      variant: {
        default: 'bg-white text-neutral-950 hover:bg-neutral-200',
        outline: 'border border-neutral-700 text-neutral-100 hover:bg-neutral-800',
        ghost: 'text-neutral-400 hover:bg-neutral-800 hover:text-white',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

export function Button({ className, variant, ...props }: ComponentProps<'button'> & VariantProps<typeof buttonVariants>) {
  return <button data-slot="button" type="button" className={twMerge(clsx(buttonVariants({ variant }), className))} {...props} />
}
