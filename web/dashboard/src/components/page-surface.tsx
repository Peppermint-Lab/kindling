import * as React from "react"
import { cn } from "@/lib/utils"

function Surface({
  className,
  variant = "card",
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement> & {
  variant?: "card" | "inset" | "ghost"
}) {
  return (
    <div
      className={cn(
        "rounded-xl",
        variant === "card" &&
          "border border-white/10 bg-white/5 backdrop-blur-sm text-card-foreground",
        variant === "inset" &&
          "border border-white/[0.06] bg-black/20 text-card-foreground",
        variant === "ghost" && "text-card-foreground",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

function SurfaceHeader({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex flex-col gap-1 px-5 pt-5 pb-4 sm:px-6 sm:pt-6", className)}
      {...props}
    >
      {children}
    </div>
  )
}

function SurfaceTitle({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3
      className={cn("text-sm font-semibold leading-none tracking-tight text-foreground", className)}
      {...props}
    >
      {children}
    </h3>
  )
}

function SurfaceDescription({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p
      className={cn("text-sm text-muted-foreground leading-relaxed", className)}
      {...props}
    >
      {children}
    </p>
  )
}

function SurfaceBody({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("px-5 pb-5 sm:px-6 sm:pb-6", className)} {...props}>
      {children}
    </div>
  )
}

function SurfaceSeparator({ className }: { className?: string }) {
  return <div className={cn("border-t border-white/[0.06] mx-5 sm:mx-6", className)} />
}

function SurfaceRow({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex flex-col gap-2 px-5 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-6",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

export {
  Surface,
  SurfaceHeader,
  SurfaceTitle,
  SurfaceDescription,
  SurfaceBody,
  SurfaceSeparator,
  SurfaceRow,
}
