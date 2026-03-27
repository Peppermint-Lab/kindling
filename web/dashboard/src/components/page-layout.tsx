import * as React from "react"
import { cn } from "@/lib/utils"
import { Link } from "react-router-dom"
import { ArrowLeftIcon } from "lucide-react"

function PageContainer({
  className,
  size = "default",
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement> & {
  size?: "default" | "wide" | "narrow"
}) {
  return (
    <div
      className={cn(
        "mx-auto w-full",
        size === "narrow" && "max-w-3xl",
        size === "default" && "max-w-5xl",
        size === "wide" && "max-w-6xl",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

function PageHeader({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex flex-col gap-4 pb-1 sm:flex-row sm:items-center sm:justify-between",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

function PageTitle({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h1
      className={cn(
        "text-2xl font-semibold tracking-tight text-foreground",
        className,
      )}
      {...props}
    >
      {children}
    </h1>
  )
}

function PageDescription({
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

function PageActions({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex flex-wrap items-center gap-2 shrink-0", className)}
      {...props}
    >
      {children}
    </div>
  )
}

function PageBackLink({
  to,
  children,
  className,
}: {
  to: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <Link
      to={to}
      className={cn(
        "inline-flex items-center gap-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground mb-3",
        className,
      )}
    >
      <ArrowLeftIcon className="size-3" />
      {children}
    </Link>
  )
}

function PageSection({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("space-y-6", className)} {...props}>
      {children}
    </div>
  )
}

function MetadataGrid({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDListElement>) {
  return (
    <dl
      className={cn("grid gap-4 sm:grid-cols-2 lg:grid-cols-3", className)}
      {...props}
    >
      {children}
    </dl>
  )
}

function MetadataItem({
  label,
  children,
  className,
  span,
}: {
  label: string
  children: React.ReactNode
  className?: string
  span?: "full" | "2"
}) {
  return (
    <div
      className={cn(
        span === "full" && "sm:col-span-2 lg:col-span-3",
        span === "2" && "sm:col-span-2",
        className,
      )}
    >
      <dt className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </dt>
      <dd className="mt-1.5 text-sm">{children}</dd>
    </div>
  )
}

function EmptyState({
  icon,
  title,
  description,
  action,
  className,
}: {
  icon?: React.ReactNode
  title: string
  description?: string
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center py-16 text-center px-4",
        className,
      )}
    >
      {icon && (
        <div className="mb-4 text-muted-foreground/60">{icon}</div>
      )}
      <p className="text-sm font-medium text-muted-foreground">{title}</p>
      {description && (
        <p className="mt-1.5 text-sm text-muted-foreground/80 max-w-sm leading-relaxed">
          {description}
        </p>
      )}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

function PageErrorBanner({
  message,
  className,
}: {
  message: string
  className?: string
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-destructive/40 bg-destructive/5 px-4 py-3 text-sm text-destructive",
        className,
      )}
    >
      {message}
    </div>
  )
}

export {
  PageContainer,
  PageHeader,
  PageTitle,
  PageDescription,
  PageActions,
  PageBackLink,
  PageSection,
  MetadataGrid,
  MetadataItem,
  EmptyState,
  PageErrorBanner,
}
