import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom"
import { AppSidebar } from "@/components/app-sidebar"
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbList,
  BreadcrumbPage,
} from "@/components/ui/breadcrumb"
import { Separator } from "@/components/ui/separator"
import {
  SidebarInset,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"
import { ProjectsPage } from "@/pages/ProjectsPage"
import { ProjectDetailPage } from "@/pages/ProjectDetailPage"
import { DeploymentDetailPage } from "@/pages/DeploymentDetailPage"
import { DeploymentsPage } from "@/pages/DeploymentsPage"
import { SettingsPage } from "@/pages/SettingsPage"
import { ServerDetailPage } from "@/pages/ServerDetailPage"
import { LoginPage } from "@/pages/LoginPage"
import { BootstrapPage } from "@/pages/BootstrapPage"
import { ThemeToggle } from "@/components/theme-toggle"
import { AuthProvider, useAuth } from "@/contexts/AuthContext"

function pageName(pathname: string): string {
  if (pathname.startsWith("/settings/servers/")) return "Server"
  if (pathname.startsWith("/deployments/")) return "Deployment"
  if (pathname === "/deployments") return "Deployments"
  if (pathname.startsWith("/projects/")) return "Project"
  if (pathname === "/settings") return "Settings"
  return "Projects"
}

function Layout() {
  const location = useLocation()
  const { session } = useAuth()

  return (
    <SidebarProvider>
      <AppSidebar />
      <SidebarInset>
        <header className="flex h-16 shrink-0 items-center gap-2 pr-2">
          <div className="flex flex-1 items-center gap-2 px-4 min-w-0">
            <SidebarTrigger className="-ml-1 shrink-0" />
            <Separator orientation="vertical" className="mr-2 h-4 hidden sm:block" />
            <Breadcrumb className="min-w-0">
              <BreadcrumbList>
                <BreadcrumbItem>
                  <BreadcrumbPage className="truncate">
                    {pageName(location.pathname)}
                  </BreadcrumbPage>
                </BreadcrumbItem>
              </BreadcrumbList>
            </Breadcrumb>
            {session && session.authenticated ? (
              <span className="text-muted-foreground hidden md:inline text-xs truncate ml-2">
                {session.organization.name}
              </span>
            ) : null}
          </div>
          <ThemeToggle />
        </header>
        <div className="flex flex-1 flex-col gap-4 p-4 pt-0">
          <Routes>
            <Route path="/" element={<ProjectsPage />} />
            <Route path="/projects" element={<ProjectsPage />} />
            <Route path="/projects/:id" element={<ProjectDetailPage />} />
            <Route path="/deployments" element={<DeploymentsPage />} />
            <Route path="/deployments/:id" element={<DeploymentDetailPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/settings/servers/:id" element={<ServerDetailPage />} />
          </Routes>
        </div>
      </SidebarInset>
    </SidebarProvider>
  )
}

function PrivateRoutes() {
  const { session, loading } = useAuth()
  if (loading) {
    return (
      <div className="flex min-h-svh items-center justify-center text-muted-foreground text-sm">
        Loading…
      </div>
    )
  }
  if (!session || !session.authenticated) {
    return <Navigate to="/login" replace />
  }
  return <Layout />
}

export default function App() {
  return (
    <TooltipProvider>
      <BrowserRouter>
        <AuthProvider>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/bootstrap" element={<BootstrapPage />} />
            <Route path="/*" element={<PrivateRoutes />} />
          </Routes>
        </AuthProvider>
      </BrowserRouter>
    </TooltipProvider>
  )
}
