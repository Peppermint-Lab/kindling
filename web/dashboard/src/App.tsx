import { lazy, Suspense, type ReactNode } from "react"
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom"
import { AppSidebar } from "@/components/app-sidebar"
import {
  SidebarInset,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"
import { ThemeToggle } from "@/components/theme-toggle"
import { AuthProvider, useAuth } from "@/contexts/AuthContext"
import type { AuthSession } from "@/lib/api"

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
const DeploymentsPage = lazy(() =>
  import("@/pages/DeploymentsPage").then((module) => ({
    default: module.DeploymentsPage,
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
const OnboardingPage = lazy(() =>
  import("@/pages/OnboardingPage").then((module) => ({
    default: module.OnboardingPage,
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

function canAccessDuringOnboarding(pathname: string) {
  return pathname === "/settings" || pathname.startsWith("/settings/")
}

type AuthenticatedSession = Extract<AuthSession, { authenticated: true }>

function mustRedirectToOnboarding(session: AuthenticatedSession, pathname: string) {
  if (!session.needs_onboarding) return false
  if (session.deployment_kind === "self_hosted" && canAccessDuringOnboarding(pathname)) {
    return false
  }
  return true
}

function SessionLoading() {
  return (
    <div className="flex min-h-svh items-center justify-center text-muted-foreground text-sm">
      Loading…
    </div>
  )
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
              path="/deployments"
              element={
                <PrivateRouteContent>
                  <DeploymentsPage />
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
  const location = useLocation()
  if (loading) {
    return <SessionLoading />
  }
  if (!session || !session.authenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }
  if (mustRedirectToOnboarding(session, location.pathname)) {
    return <Navigate to="/onboarding" replace />
  }
  return <Layout />
}

function OnboardingRoute() {
  const { session, loading } = useAuth()
  const location = useLocation()
  if (loading) {
    return <SessionLoading />
  }
  if (!session || !session.authenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />
  }
  if (!session.needs_onboarding) {
    return <Navigate to="/" replace />
  }
  return (
    <Suspense fallback={<PrivateRouteFallback />}>
      <OnboardingPage />
    </Suspense>
  )
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
              <Route path="/onboarding" element={<OnboardingRoute />} />
              <Route path="/*" element={<PrivateRoutes />} />
            </Routes>
          </Suspense>
        </AuthProvider>
      </BrowserRouter>
    </TooltipProvider>
  )
}
