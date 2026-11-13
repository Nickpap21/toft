import * as React from "react"
import { cn } from "@/lib/utils"

const Button = React.forwardRef<
  HTMLButtonElement,
  React.ButtonHTMLAttributes<HTMLButtonElement> & {
    variant?: "default" | "destructive" | "outline" | "secondary" | "ghost" | "link"
    size?: "default" | "sm" | "lg" | "icon"
  }
>(({ className, variant = "default", size = "default", ...props }, ref) => {
  return (
    <button
      className={cn(
        "touch-target inline-flex items-center justify-center whitespace-nowrap rounded-lg text-sm font-medium transition-colors cursor-pointer",
        // The focus outline comes from the base layer (globals.css).
        "disabled:pointer-events-none disabled:opacity-50 disabled:cursor-not-allowed",
        {
          // Default — the screen's main action, on the theme's accent
          "bg-accent text-on-accent hover:bg-accent/90": variant === "default",
          // Destructive — solid danger; dark text on dark themes (D-32)
          "bg-danger text-on-danger hover:bg-danger/90": variant === "destructive",
          // Outline — bordered, no fill
          "border border-strong bg-transparent text-fg hover:bg-raised": variant === "outline",
          // Secondary — muted fill
          "bg-raised text-fg hover:bg-raised/70": variant === "secondary",
          // Ghost — no border, no fill
          "text-muted hover:bg-raised hover:text-fg": variant === "ghost",
          // Link — text style
          "text-link underline-offset-4 hover:underline": variant === "link",
          // Sizes
          "h-10 px-4 py-2": size === "default",
          "h-9 rounded-lg px-3 text-xs": size === "sm",
          "h-11 rounded-lg px-8": size === "lg",
          "h-10 w-10 p-0 pointer-coarse:min-w-11": size === "icon",
        },
        className
      )}
      ref={ref}
      {...props}
    />
  )
})
Button.displayName = "Button"

export { Button }
