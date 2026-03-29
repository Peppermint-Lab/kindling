import { useState } from "react"
import { Flame, Menu, X } from "lucide-react"
import { loginHref } from "@/lib/login"

function GitHubIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" className={className}>
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
    </svg>
  )
}

const links = [
  { label: "docs", href: "https://docs.kindling.systems" },
  { label: "github", href: "https://github.com/kindlingvm/kindling" },
]

export function Navbar() {
  const [open, setOpen] = useState(false)

  return (
    <nav className="border-b border-gray-200 sticky top-0 bg-white z-50">
      <div className="mx-auto max-w-3xl px-6 flex items-center justify-between h-14">
        <a href="/" className="flex items-center gap-2 text-black">
          <Flame className="w-4 h-4" />
          <span className="font-semibold text-sm">Kindling</span>
        </a>

        <div className="hidden sm:flex items-center gap-6 text-sm">
          {links.map((l) => (
            <a
              key={l.label}
              href={l.href}
              className="text-gray-500 hover:text-black transition-colors"
            >
              {l.label}
            </a>
          ))}
          <a
            href="https://github.com/kindlingvm/kindling"
            className="text-gray-500 hover:text-black transition-colors"
          >
            <GitHubIcon className="w-4 h-4" />
          </a>
          <a
            href={loginHref}
            className="text-sm font-medium text-black bg-gray-100 hover:bg-gray-200 px-3 py-1.5 rounded-md transition-colors"
          >
            Sign in
          </a>
        </div>

        <button
          className="sm:hidden text-gray-500 hover:text-black"
          onClick={() => setOpen(!open)}
        >
          {open ? <X className="w-5 h-5" /> : <Menu className="w-5 h-5" />}
        </button>
      </div>

      {open && (
        <div className="sm:hidden border-t border-gray-100 px-6 py-3 flex flex-col gap-2 text-sm">
          {links.map((l) => (
            <a
              key={l.label}
              href={l.href}
              className="text-gray-500 hover:text-black py-1"
              onClick={() => setOpen(false)}
            >
              {l.label}
            </a>
          ))}
          <a
            href={loginHref}
            className="text-sm font-medium text-black py-1"
            onClick={() => setOpen(false)}
          >
            Sign in
          </a>
        </div>
      )}
    </nav>
  )
}
