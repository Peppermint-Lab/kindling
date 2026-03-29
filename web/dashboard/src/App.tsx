import { lazy, Suspense, type ReactNode } from "react"
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
import { ThemeToggle } from "@/components/theme-toggle"
import { AuthProvider, useAuth } from "@/contexts/AuthContext"

const ProjectsPage = lazy(() =>
  import("@/pages/ProjectsPage").then((module) => ({
    default: module.ProjectsPage,
  }))
)
const ProjectDetailPage = lazy(() =>
  import("@/pages/ProjectDetailPage").then((module) => ({
    default: module.ProjectDetailPage,
  }))
)
const ServiceDetailPage = lazy(() =>
  import("@/pages/ServiceDetailPage").then((module) => ({
    default: module.ServiceDetailPage,
  }))
)
const DeploymentsPage = lazy(() =>
  import("@/pages/DeploymentsPage").then((module) => ({
    default: module.DeploymentsPage,
  }))
)
const DeploymentDetailPage = lazy(() =>
  import("@/pages/DeploymentDetailPage").then((module) => ({
    default: module.DeploymentDetailPage,
  }))
)
const CIJobDetailPage = lazy(() =>
  import("@/pages/CIJobDetailPage").then((module) => ({
    default: module.CIJobDetailPage,
  }))
)
const SettingsPage = lazy(() =>
  import("@/pages/SettingsPage").then((module) => ({
    default: module.SettingsPage,
  }))
)
const ServerDetailPage = lazy(() =>
  import("@/pages/ServerDetailPage").then((module) => ({
    default: module.ServerDetailPage,
  }))
)
const LoginPage = lazy(() =>
  import("@/pages/LoginPage").then((module) => ({
    default: module.LoginPage,
  }))
)
const BootstrapPage = lazy(() =>
  import("@/pages/BootstrapPage").then((module) => ({
    default: module.BootstrapPage,
  }))
)

function pageName(pathname: string): string {
  if (pathname.startsWith("/settings/servers/")) return "Server"
  if (pathname.startsWith("/ci/jobs/")) return "CI Job"
  if (pathname.startsWith("/deployments/")) return "Deployment"
  if (pathname === "/deployments") return "Deployments"
  if (pathname.startsWith("/services/")) return "Service"
  if (pathname.startsWith("/projects/")) return "Project"
  if (pathname === "/settings") return "Settings"
  return "Projects"
}

function PublicRouteFallback() {
  return (
    <div className="flex min-h-svh items-center justify-center text-muted-foreground text-sm">
      Loading page…
    </div>
  )
}

function PrivateRouteFallback() {
  return (
    <div className="flex min-h-[50vh] items-center justify-center text-muted-foreground text-sm">
      Loading page…
    </div>
  )
}

function PrivateRouteContent({ children }: { children: ReactNode }) {
  return <Suspense fallback={<PrivateRouteFallback />}>{children}</Suspense>
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
            <Route
              path="/"
              element={
                <PrivateRouteContent>
                  <ProjectsPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/projects"
              element={
                <PrivateRouteContent>
                  <ProjectsPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/projects/:id"
              element={
                <PrivateRouteContent>
                  <ProjectDetailPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/services/:id"
              element={
                <PrivateRouteContent>
                  <ServiceDetailPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/deployments"
              element={
                <PrivateRouteContent>
                  <DeploymentsPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/deployments/:id"
              element={
                <PrivateRouteContent>
                  <DeploymentDetailPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/ci/jobs/:id"
              element={
                <PrivateRouteContent>
                  <CIJobDetailPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/settings"
              element={
                <PrivateRouteContent>
                  <SettingsPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/settings/servers/:id"
              element={
                <PrivateRouteContent>
                  <ServerDetailPage />
                </PrivateRouteContent>
              }
            />
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
          <Suspense fallback={<PublicRouteFallback />}>
            <Routes>
              <Route path="/login" element={<LoginPage />} />
              <Route path="/bootstrap" element={<BootstrapPage />} />
              <Route path="/*" element={<PrivateRoutes />} />
            </Routes>
          </Suspense>
        </AuthProvider>
      </BrowserRouter>
    </TooltipProvider>
  )
}
