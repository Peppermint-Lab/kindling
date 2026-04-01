// HTTP load for a static site with staged named profiles.
//
// Env:
//   TARGET_URL      (default https://kindling.systems)
//   PROFILE         smoke | medium | large | viral-4000
//   QUICK=1         backward-compatible smoke override
//   MAX_VUS         optional peak override for staged profiles
//
// Run: ./contrib/loadtest/run.sh

import http from "k6/http";
import { check, sleep } from "k6";
import { buildOptions, resolveTarget } from "./k6-config.mjs";

const target = resolveTarget(__ENV);

export const options = buildOptions(__ENV);

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
