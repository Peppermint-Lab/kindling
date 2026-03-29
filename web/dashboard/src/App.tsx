import { lazy, Suspense, type ReactNode } from "react"
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom"
import { AppSidebar } from "@/components/app-sidebar"
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
const SandboxesPage = lazy(() =>
  import("@/pages/SandboxesPage").then((module) => ({
    default: module.SandboxesPage,
  }))
)
const SandboxDetailPage = lazy(() =>
  import("@/pages/SandboxDetailPage").then((module) => ({
    default: module.SandboxDetailPage,
  }))
)
const PipelinesPage = lazy(() =>
  import("@/pages/PipelinesPage").then((module) => ({
    default: module.PipelinesPage,
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
const SSHKeysPage = lazy(() =>
  import("@/pages/SSHKeysPage").then((module) => ({
    default: module.SSHKeysPage,
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
  const { session } = useAuth()

  return (
    <SidebarProvider>
      <AppSidebar />
      <SidebarInset>
        <header className="flex h-12 shrink-0 items-center gap-2 pr-3">
          <div className="flex flex-1 items-center gap-2 px-3 min-w-0">
            <SidebarTrigger className="-ml-1 shrink-0" />
            {session && session.authenticated ? (
              <span className="text-muted-foreground hidden md:inline text-xs truncate">
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
              path="/sandboxes"
              element={
                <PrivateRouteContent>
                  <SandboxesPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/sandboxes/:id"
              element={
                <PrivateRouteContent>
                  <SandboxDetailPage />
                </PrivateRouteContent>
              }
            />
            <Route
              path="/pipelines"
              element={
                <PrivateRouteContent>
                  <PipelinesPage />
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
              path="/settings/ssh-keys"
              element={
                <PrivateRouteContent>
                  <SSHKeysPage />
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
