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

```bash
# with the browser panel
docker build --build-arg INSTALL_CHROMIUM=true -t piforge:dev .
```

Chromium is downloaded with a retry loop, because the two big packages
(`chromium`, `libllvm15`) drop mid-transfer on some links. Without it the browser
panel reports itself unavailable and everything else works.

## Run

```bash
docker run -d --init -p 7700:7700 \
  -e PIFORGE_SEED_EMAIL=admin@piforge.local \
  -e PIFORGE_SEED_PASSWORD=change-me \
  -e PIFORGE_MASTER_KEY="$(openssl rand -base64 32)" \
  -v piforge-data:/data \
  piforge:dev
```

`-e HTTPS_PROXY=...` is needed when the container cannot reach the model endpoint
directly; the model gateway runs inside the container and inherits it.

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
