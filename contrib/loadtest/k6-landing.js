// HTTP load for a static site — default scenario models a viral / “front page” spike
// (e.g. big marketing push, Reddit thread).
//
// Env:
//   TARGET_URL   (default https://kindling.systems)
//   QUICK=1      short smoke (~15s)
//   MAX_VUS      peak virtual users for the viral scenario (default 4000)
//
// Run: ./contrib/loadtest/run.sh

import http from "k6/http";
import { check, sleep } from "k6";

const target = __ENV.TARGET_URL || "https://kindling.systems";
const quick = __ENV.QUICK === "1";

const maxVus = Math.max(50, parseInt(__ENV.MAX_VUS || "4000", 10) || 4000);
const s = (frac) => Math.max(1, Math.floor(maxVus * frac));

/** Reddit-style: fast ramp as clicks pour in, long hold at peak, then decay. */
const viralStages = [
  { duration: "30s", target: s(0.08) }, // first wave
  { duration: "45s", target: s(0.2) },
  { duration: "45s", target: s(0.45) }, // thread accelerates
  { duration: "2m", target: s(0.45) },
  { duration: "1m", target: s(0.75) },
  { duration: "90s", target: maxVus }, // crush: at peak
  { duration: "5m", target: maxVus }, // sustained “everyone’s sharing the link”
  { duration: "2m", target: s(0.35) }, // trailing traffic
  { duration: "90s", target: 0 },
];

export const options = quick
  ? {
      vus: 5,
      duration: "15s",
      thresholds: {
        http_req_failed: ["rate<0.05"],
        http_req_duration: ["p(95)<5000"],
      },
    }
  : {
      stages: viralStages,
      // Under real viral load, latency often spikes before errors — don’t fail the run on first slowdown.
      thresholds: {
        http_req_failed: ["rate<0.15"],
        http_req_duration: ["p(95)<15000"],
      },
    };

export default function () {
  const res = http.get(target, {
    tags: { name: "target" },
    redirects: 5,
    timeout: "60s",
  });
  check(res, {
    "2xx/3xx": (r) => r.status >= 200 && r.status < 400,
  });
  // Short think time: lots of “open tab, read headline, bounce” rather than slow readers.
  sleep(0.05 + Math.random() * 0.35);
}
