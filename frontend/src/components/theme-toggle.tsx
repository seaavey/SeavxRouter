import { Moon, Sun } from 'lucide-react'
import { useTheme } from '@/components/theme-provider'
import { Button } from '@/components/ui/button'

/**
 * Two themes, one button: it shows the theme you would switch to, and pressing
 * it applies that. Nothing to open, nothing to read.
 */
export function ThemeToggle() {
  const { theme, toggle } = useTheme()
  const dark = theme === 'dark'
  const Icon = dark ? Sun : Moon

  return (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={`Switch to ${dark ? 'light' : 'dark'} theme`}
      title={`Switch to ${dark ? 'light' : 'dark'} theme`}
      onClick={toggle}
    >
      <Icon className="size-4" />
    </Button>
  )
}
