import * as React from "react"
import { Link, useNavigate } from "react-router-dom"

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
import {
  FlameIcon,
  FolderIcon,
  GitBranchIcon,
  RocketIcon,
  Settings2Icon,
  LifeBuoyIcon,
  BookOpenIcon,
  LogOutIcon,
  BoxIcon,
} from "lucide-react"
import { useAuth } from "@/contexts/AuthContext"
import { Button } from "@/components/ui/button"
import type { AuthOrganization } from "@/lib/api"
import { kindlingVersion } from "@/lib/version"

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
      title: "Sandboxes",
      url: "/sandboxes",
      icon: <BoxIcon />,
    },
    {
      title: "Pipelines",
      url: "/pipelines",
      icon: <GitBranchIcon />,
    },
    {
      title: "Settings",
      url: "/settings",
      icon: <Settings2Icon />,
      items: [
        {
          title: "SSH Keys",
          url: "/settings/ssh-keys",
        },
      ],
    },
  ],
  navSecondary: [
    {
      title: "Documentation",
      url: "https://docs.kindling.systems",
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
  const { logout, session, switchOrg } = useAuth()
  const navigate = useNavigate()

  return (
    <Sidebar variant="inset" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton size="lg" render={<Link to="/" />}>
              <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
                <FlameIcon className="size-4" />
              </div>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">Kindling</span>
                <span className="truncate text-xs">Self-hosted PaaS</span>
                <span className="truncate font-mono text-[11px] text-sidebar-foreground/60">
                  {kindlingVersion.tag}
                </span>
              </div>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        <NavMain items={data.navMain} />
        {session && session.authenticated && session.organizations.length > 1 ? (
          <div className="px-2 py-2 border-t border-sidebar-border">
            <p className="text-xs text-muted-foreground mb-1 px-2">Organization</p>
            <select
              className="w-full text-sm rounded-md border bg-background px-2 py-1.5"
              value={session.organization.id}
              onChange={(e) => {
                void switchOrg(e.target.value)
              }}
            >
              {session.organizations.map((o: AuthOrganization) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
            </select>
          </div>
        ) : null}
        <NavSecondary items={data.navSecondary} className="mt-auto" />
        <div className="p-2 border-t border-sidebar-border">
          <Button
            variant="ghost"
            size="sm"
            className="w-full justify-start gap-2"
            onClick={() => {
              void logout().then(() => navigate("/login"))
            }}
          >
            <LogOutIcon className="size-4" />
            Sign out
          </Button>
        </div>
      </SidebarContent>
    </Sidebar>
  )
}
