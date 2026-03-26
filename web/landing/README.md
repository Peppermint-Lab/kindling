# Kindling landing

Static marketing site built with Vite + React.

## Deploy on Kindling (OVH / production)

1. Point a Kindling project at this repo with **`RootDirectory`** set to **`/web/landing`** (leading slash as stored in the DB).
2. Keep **`DockerfilePath`** as **`Dockerfile`** (default). The build uses the `Dockerfile` in this directory once the tarball is filtered to the root directory.
3. Deploy as usual: the image serves the built SPA on **port 3000** over HTTP (Kindling health checks and guest agent expect this).

### IP-only URL (no custom domain yet)

Cloud Hypervisor (and similar runtimes) publish the app on **`http://<public-host>:<ephemeral-port>/`**. To make that host your OVH public IP:

1. Start Kindling with a one-time seed (only applies while `server_settings.advertise_host` is empty):
   - `./bin/kindling serve --advertise-host YOUR_PUBLIC_IP`
   - or `KINDLING_RUNTIME_ADVERTISE_HOST=YOUR_PUBLIC_IP ./bin/kindling serve`
2. Allow inbound TCP to the kernel’s ephemeral port range on the host (where the forwarder listens). On Linux, check `cat /proc/sys/net/ipv4/ip_local_port_range` — often **32768–60999**. Example with UFW: `sudo ufw allow 32768:60999/tcp` (tighten later if you move to a fixed edge port).
3. Trigger a deploy, then open the **`runtime_url`** from the dashboard or API (e.g. `http://203.0.113.7:45231/`). No edge proxy or DNS record is required for this path.

Local checks: `npm ci && npm run build`. Optional: `docker build -t kindling-landing:local .` and run with `-p 3000:3000`.
