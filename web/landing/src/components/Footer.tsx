import { Flame } from "lucide-react"
import { loginHref } from "@/lib/login"

export function Footer() {
  return (
    <footer className="border-t border-gray-200 mt-8">
      <div className="mx-auto max-w-3xl px-6 py-10">
        <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
          <div className="flex items-center gap-2 text-sm text-gray-400">
            <Flame className="w-3.5 h-3.5" />
            <span>Kindling</span>
          </div>
          <div className="flex items-center gap-6 text-sm text-gray-400">
            <a
              href={loginHref}
              className="hover:text-black transition-colors"
            >
              sign in
            </a>
            <a
              href="https://docs.kindling.dev"
              className="hover:text-black transition-colors"
            >
              docs
            </a>
            <a
              href="https://github.com/kindlingvm/kindling"
              className="hover:text-black transition-colors"
            >
              github
            </a>
            <a
              href="https://github.com/kindlingvm/kindling/discussions"
              className="hover:text-black transition-colors"
            >
              community
            </a>
          </div>
        </div>
      </div>
    </footer>
  )
}
