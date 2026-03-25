const STORAGE_KEY = "kindling-theme"

function applyTheme(mode: "light" | "dark") {
  document.documentElement.classList.toggle("dark", mode === "dark")
  localStorage.setItem(STORAGE_KEY, mode)
}

/** Hydrate theme from localStorage or system preference (call once on boot). */
export function initTheme() {
  const stored = localStorage.getItem(STORAGE_KEY) as "light" | "dark" | null
  if (stored === "light" || stored === "dark") {
    applyTheme(stored)
    return
  }
  const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches
  applyTheme(prefersDark ? "dark" : "light")
}

export function toggleStoredTheme(): "light" | "dark" {
  const next = document.documentElement.classList.contains("dark") ? "light" : "dark"
  applyTheme(next)
  return next
}
