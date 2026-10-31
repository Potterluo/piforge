# Deploying PiForge

The image runs the control plane, the web app and the sandbox itself: the local
sandbox provider starts the agent, the shells a user opens and the browser they
watch as children of the control plane, so **the container is the sandbox
boundary**. One process tree, one volume, one port.

## Build

```bash
docker build -t piforge:dev .
```

Two build arguments matter:

| Argument | Default | Why |
|---|---|---|
| `NPM_REGISTRY` | `https://registry.npmmirror.com` | A CN build box cannot reach the npm registry reliably. |
| `GOPROXY` | `https://goproxy.cn,direct` | Same, for Go modules. |
| `APT_MIRROR` | `http://mirrors.aliyun.com` | `deb.debian.org` is often unreachable; a TLS-terminating proxy also breaks apt's signature check. |
| `INSTALL_CHROMIUM` | `false` | Adds ~250 MB and the browser panel. Opt-in because it is the one part of the image that fails on a flaky link. |
| `CJK_FONTS` | `true` | Installs `fonts-noto-cjk` for the sandbox browser. Without it every Chinese page renders as empty boxes inside the container. |

The font package is fetched with `curl -C -` rather than by apt: it is a ~50 MB
transfer, apt restarts it from zero on every failure, and this link drops.

```bash
# with the browser panel
docker build --build-arg INSTALL_CHROMIUM=true -t piforge:dev .
```

Chromium is downloaded with a retry loop, because the two big packages
(`chromium`, `libllvm15`) drop mid-transfer on some links. Without it the browser
panel reports itself unavailable and everything else works.

## Iterate against a published base

The full build takes ~20 minutes, and 19 of those are Node, pi and Chromium — none
of which change while working on the product. Build the stack once, publish it,
then layer this repository on top:

```bash
# once, on a machine with a real internet route
docker build --build-arg INSTALL_CHROMIUM=true -t ccr.ccs.tencentyun.com/<ns>/piforge:dev .
docker push ccr.ccs.tencentyun.com/<ns>/piforge:dev

# every time after that (Windows-friendly)
pwsh scripts/build-app-image.ps1                  # ~50 s
pwsh scripts/build-app-image.ps1 -Tag piforge:pr  # different tag
```

`scripts/build-app-image.ps1` cross-compiles the control plane for linux/amd64,
builds the web app, stages the two artifacts and builds `deploy/Dockerfile.app`
on top of the published base.

One trap it handles for you: `web/.env.local` points the bundle at the
development API port, and Next inlines `NEXT_PUBLIC_*` at build time. A bundle
built with that file present sends the terminal and browser sockets to a port the
container never listens on — the panel then shows "已断开" and nothing else. The
script moves the file aside for the duration of the build.

## Run

```bash
docker run -d --init -p 7700:7700 \
  --cap-drop DAC_OVERRIDE \
  -e PIFORGE_SEED_EMAIL=admin@piforge.local \
  -e PIFORGE_SEED_PASSWORD=change-me \
  -e PIFORGE_MASTER_KEY="$(openssl rand -base64 32)" \
  -v piforge-data:/data \
  piforge:dev
```

`-e HTTPS_PROXY=...` is needed when the container cannot reach the model endpoint
directly; the model gateway runs inside the container and inherits it.

**Do not add `--cap-add SYS_ADMIN`, and do keep `--cap-drop DAC_OVERRIDE`.**
Skills are projected into a session as a read-only copy (`copy-ro`) by default.
Copying marks the files 0444/0555, and dropping `CAP_DAC_OVERRIDE` is what makes
those bits hold for the root user the image runs as; every file the control plane
writes is its own, so the capability buys nothing. `SYS_ADMIN` only upgrades the
mechanism to a read-only bind mount (`bind-ro`): the kernel enforces it and
sessions share one frozen snapshot instead of each holding a copy. It changes the
mechanism, not whether the tree is read-only, and a broad capability weakens the
container boundary. The badge in the session's info sheet names what is actually
in effect (「只读挂载」 vs 「只读副本」). Without the `cap_drop` the product still
starts, but reports `copy` (frozen but writable) instead of pretending.

## Two containers: moving the Docker socket out of the control plane

The image above runs the sandbox as a local directory, so the container *is* the
boundary. If you want one container per project, the control plane needs a Docker
daemon — and the socket is host root, so it must not be this container. The
`sandbox-runner` service owns the socket instead:

```bash
export SANDBOX_RUNNER_TOKEN="$(openssl rand -hex 32)"
export PIFORGE_SANDBOX_IMAGE=ghcr.io/agent-infra/sandbox:1.11.0
docker compose -f docker-compose.yml -f docker-compose.runner.yml up -d --build
```

Only `sandbox-runner` mounts `/var/run/docker.sock`; `piforge` talks to it over
`http://sandbox-runner:7801` with that token. Both mount the same data volume at
`/data`, at the same path, because each sandbox's workspace is a bind mount: the
runner creates the container with a host path, and the control plane has to name
the very same directory. `docker-compose.runner.yml` carries the full comment.

The image is `deploy/Dockerfile.runner` (`piforge/sandbox-runner:dev`). It has the
Docker CLI and the runner binary, nothing else.

## Publish

```bash
docker login ccr.ccs.tencentyun.com --username=<id>
docker tag piforge:dev ccr.ccs.tencentyun.com/<namespace>/piforge:dev
docker push ccr.ccs.tencentyun.com/<namespace>/piforge:dev
```

Then either:

```bash
PIFORGE_IMAGE=ccr.ccs.tencentyun.com/<namespace>/piforge:dev docker compose up -d
```

or apply `k8s.yaml` with the `image:` field pointed at the pushed tag.

## Kubernetes

```bash
kubectl apply -f deploy/k8s.yaml
kubectl -n piforge port-forward svc/piforge 7700:7700
```

One replica, `strategy: Recreate`, `ReadWriteOnce` volume. That is not a
limitation to work around: the pod holds live processes and terminal sessions, so
a second replica would answer requests for sessions it does not own. Horizontal
scale needs a shared event bus and session affinity, which is on the "not here
yet" list in the README.

## What the container does *not* do

- **No horizontal scale.** See above.
- **No TLS termination.** Put it behind an ingress or a load balancer; the
  control plane speaks plain HTTP and sets cookies with `-secure-cookies` when it
  is behind one.
- **Bubblewrap isolation is optional.** The image can run `-isolation bubblewrap`
  (`PIFORGE_ISOLATION=bubblewrap`) when the container has the needed capabilities;
  the default is `none`, because the container is already the boundary.
