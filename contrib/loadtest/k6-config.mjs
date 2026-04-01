const DEFAULT_TARGET_URL = "https://kindling.systems";
const DEFAULT_PROFILE = "viral-4000";
const DEFAULT_MAX_VUS = 4000;

function intFromEnv(value, fallback) {
  const parsed = parseInt(value || "", 10);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function peakForProfile(profile, env) {
  const override = intFromEnv(env.MAX_VUS, 0);
  if (override > 0 && profile !== "smoke") {
    return Math.max(50, override);
  }

  switch (profile) {
    case "medium":
      return 300;
    case "large":
      return 1000;
    case "viral-4000":
      return DEFAULT_MAX_VUS;
    default:
      return DEFAULT_MAX_VUS;
  }
}

function stagedScenario(maxVus) {
  const s = (frac) => Math.max(1, Math.floor(maxVus * frac));

  return [
    { duration: "30s", target: s(0.08) },
    { duration: "45s", target: s(0.2) },
    { duration: "45s", target: s(0.45) },
    { duration: "2m", target: s(0.45) },
    { duration: "1m", target: s(0.75) },
    { duration: "90s", target: maxVus },
    { duration: "5m", target: maxVus },
    { duration: "2m", target: s(0.35) },
    { duration: "90s", target: 0 },
  ];
}

function mediumStages(maxVus) {
  const s = (frac) => Math.max(1, Math.floor(maxVus * frac));

  return [
    { duration: "20s", target: s(0.15) },
    { duration: "40s", target: s(0.5) },
    { duration: "60s", target: maxVus },
    { duration: "45s", target: s(0.35) },
    { duration: "20s", target: 0 },
  ];
}

function largeStages(maxVus) {
  const s = (frac) => Math.max(1, Math.floor(maxVus * frac));

  return [
    { duration: "25s", target: s(0.1) },
    { duration: "40s", target: s(0.35) },
    { duration: "50s", target: s(0.7) },
    { duration: "90s", target: maxVus },
    { duration: "45s", target: s(0.4) },
    { duration: "30s", target: 0 },
  ];
}

export function resolveProfile(env = {}) {
  if (env.QUICK === "1") {
    return "smoke";
  }
  return env.PROFILE || DEFAULT_PROFILE;
}

export function buildOptions(env = {}) {
  const profile = resolveProfile(env);

  if (profile === "smoke") {
    return {
      vus: 5,
      duration: "15s",
      thresholds: {
        http_req_failed: ["rate<0.05"],
        http_req_duration: ["p(95)<5000"],
      },
    };
  }

  const maxVus = peakForProfile(profile, env);
  let stages;

  switch (profile) {
    case "medium":
      stages = mediumStages(maxVus);
      break;
    case "large":
      stages = largeStages(maxVus);
      break;
    case "viral-4000":
      stages = stagedScenario(maxVus);
      break;
    default:
      throw new Error(
        `unknown PROFILE ${profile}; expected smoke, medium, large, or viral-4000`,
      );
  }

  return {
    stages,
    thresholds: {
      http_req_failed: ["rate<0.15"],
      http_req_duration: ["p(95)<15000"],
    },
  };
}

export function resolveTarget(env = {}) {
  return env.TARGET_URL || DEFAULT_TARGET_URL;
}
