import { Tabs as TabsPrimitive } from "@base-ui/react/tabs"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

function Tabs({
  className,
  orientation = "horizontal",
  ...props
}: TabsPrimitive.Root.Props) {
  return (
    <TabsPrimitive.Root
      data-slot="tabs"
      data-orientation={orientation}
      className={cn(
        "group/tabs flex gap-2 data-horizontal:flex-col data-vertical:max-md:flex-col",
        className
      )}
      {...props}
    />
  )
}

const tabsListVariants = cva(
  "group/tabs-list inline-flex w-fit items-center justify-center rounded-lg p-[3px] text-muted-foreground group-data-horizontal/tabs:h-8 group-data-vertical/tabs:max-md:h-8 group-data-vertical/tabs:md:h-fit group-data-vertical/tabs:md:flex-col group-data-vertical/tabs:md:items-stretch data-[variant=line]:rounded-none",
  {
    variants: {
      variant: {
        default: "bg-white/[0.04] border border-white/[0.06]",
        line: "gap-1 bg-transparent group-data-vertical/tabs:md:gap-0.5",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function TabsList({
  className,
  variant = "default",
  ...props
}: TabsPrimitive.List.Props & VariantProps<typeof tabsListVariants>) {
  return (
    <TabsPrimitive.List
      data-slot="tabs-list"
      data-variant={variant}
      className={cn(tabsListVariants({ variant }), className)}
      {...props}
    />
  )
}

function TabsTrigger({ className, ...props }: TabsPrimitive.Tab.Props) {
  return (
    <TabsPrimitive.Tab
      data-slot="tabs-trigger"
      className={cn(
        "relative inline-flex h-[calc(100%-1px)] flex-1 items-center justify-center gap-1.5 rounded-md border border-transparent px-3 py-1 text-xs font-medium whitespace-nowrap transition-all group-data-vertical/tabs:flex-none group-data-vertical/tabs:md:w-full group-data-vertical/tabs:md:h-auto group-data-vertical/tabs:md:justify-start group-data-vertical/tabs:md:px-3 group-data-vertical/tabs:md:py-2 hover:text-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-1 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 text-muted-foreground hover:text-foreground group-data-[variant=default]/tabs-list:data-active:shadow-none group-data-[variant=line]/tabs-list:data-active:shadow-none [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        "group-data-[variant=line]/tabs-list:bg-transparent group-data-[variant=line]/tabs-list:data-active:bg-transparent dark:group-data-[variant=line]/tabs-list:data-active:border-transparent dark:group-data-[variant=line]/tabs-list:data-active:bg-transparent",
        "data-active:bg-white/[0.08] data-active:text-foreground dark:data-active:border-white/10 dark:data-active:bg-white/[0.06] dark:data-active:text-foreground",
        "after:absolute after:opacity-0 after:transition-opacity group-data-horizontal/tabs:after:inset-x-0 group-data-horizontal/tabs:after:bottom-[-5px] group-data-horizontal/tabs:after:h-0.5 group-data-vertical/tabs:max-md:after:inset-x-0 group-data-vertical/tabs:max-md:after:bottom-[-5px] group-data-vertical/tabs:max-md:after:h-0.5 group-data-vertical/tabs:md:after:inset-y-0 group-data-vertical/tabs:md:after:-right-1 group-data-vertical/tabs:md:after:w-0.5 group-data-[variant=line]/tabs-list:data-active:after:opacity-100",
        "group-data-[variant=default]/tabs-list:data-active:after:bg-gradient-to-r group-data-[variant=default]/tabs-list:data-active:after:from-[#f97316] group-data-[variant=default]/tabs-list:data-active:after:to-[#f59e0b] group-data-[variant=default]/tabs-list:data-active:after:opacity-100 group-data-[variant=default]/tabs-list:data-active:after:h-0.5 group-data-[variant=default]/tabs-list:data-active:after:rounded-full",
        className
      )}
      {...props}
    />
  )
}

function TabsContent({ className, ...props }: TabsPrimitive.Panel.Props) {
  return (
    <TabsPrimitive.Panel
      data-slot="tabs-content"
      className={cn("flex-1 text-sm outline-none", className)}
      {...props}
    />
  )
}

export { Tabs, TabsList, TabsTrigger, TabsContent, tabsListVariants }
