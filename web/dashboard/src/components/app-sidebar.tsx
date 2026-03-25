import * as React from "react"

import { NavMain } from "@/components/nav-main"
import { NavSecondary } from "@/components/nav-secondary"
import {
  Sidebar,
  SidebarContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar"
import { FlameIcon, FolderIcon, RocketIcon, Settings2Icon, LifeBuoyIcon, BookOpenIcon } from "lucide-react"

const data = {
  navMain: [
    {
      title: "Projects",
      url: "/projects",
      icon: <FolderIcon />,
      isActive: true,
    },
    {
      title: "Deployments",
      url: "/deployments",
      icon: <RocketIcon />,
    },
    {
      title: "Settings",
      url: "/settings",
      icon: <Settings2Icon />,
    },
  ],
  navSecondary: [
    {
      title: "Documentation",
      url: "https://docs.kindling.dev",
      icon: <BookOpenIcon />,
    },
    {
      title: "Support",
      url: "#",
      icon: <LifeBuoyIcon />,
    },
  ],
}

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  return (
    <Sidebar variant="inset" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" render={<a href="/" />}>
              <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                <FlameIcon className="size-4" />
              </div>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">Kindling</span>
                <span className="truncate text-xs">Self-hosted PaaS</span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={data.navMain} />
        <NavSecondary items={data.navSecondary} className="mt-auto" />
      </SidebarContent>
    </Sidebar>
  )
}
