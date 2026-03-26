import { Navbar } from "@/components/Navbar"
import { Hero } from "@/components/Hero"
import { Terminal } from "@/components/Terminal"
import { Features } from "@/components/Features"
import { Comparison } from "@/components/Comparison"
import { FAQ } from "@/components/FAQ"
import { Footer } from "@/components/Footer"

export default function App() {
  return (
    <>
      <Navbar />
      <main>
        <Hero />
        <Terminal />
        <Features />
        <Comparison />
        <FAQ />
      </main>
      <Footer />
    </>
  )
}
