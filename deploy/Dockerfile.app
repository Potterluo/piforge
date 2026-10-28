# syntax=docker/dockerfile:1

# Iteration image.
#
# The published image already carries the heavy, slow-to-build stack — Node, pi,
# Chromium, git and the npm tree — so this one only replaces this repository's own
# artifacts. A rebuild takes seconds instead of the twenty minutes a full build
# needs, which is the difference between iterating and waiting.
#
# The build context is a staged directory, not the repository: `.dockerignore`
# exists to keep `web/.next` and the compiled binaries *out* of the full build,
# and this image needs exactly those. Build it with:
#
#   pwsh scripts/build-app-image.ps1          # stages, builds piforge:local
#   pwsh scripts/build-app-image.ps1 -NoBuild # reuse existing artifacts
#
# Override the base with --build-arg BASE=... to test against a different stack.
ARG BASE=registry.example.com/piforge/piforge:dev
FROM ${BASE}

# CJK fonts for the sandbox browser. Debian's slim images carry no CJK glyphs, so
# every Chinese page inside the container rendered as empty boxes while the same
# page looked fine in the operator's own browser — a font is part of the sandbox,
# not a detail. The canonical Dockerfile installs these too; this layer exists so
# an image built on an older published base still gets them.
# The published base may still point apt at deb.debian.org, which is unreachable
# from here, so the mirror rewrite is repeated instead of assumed. Only the base
# CJK package: `-extra` doubles the download for glyphs a browser rarely needs.
ARG APT_MIRROR=http://mirrors.aliyun.com
# CJK fonts are 50 MB of download. Keeping them behind a switch lets a demo image
# skip them without pretending the default is right: an image without them shows
# tofu for every Chinese page.
ARG CJK_FONTS=true
RUN set -e; \
    if [ "$CJK_FONTS" = "true" ] && [ ! -f /usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc ]; then \
      sed -i "s|http://deb.debian.org/debian|${APT_MIRROR}/debian|g; s|http://deb.debian.org/debian-security|${APT_MIRROR}/debian-security|g" \
        /etc/apt/sources.list.d/debian.sources; \
      printf 'Acquire::http::Pipeline-Depth "0";\nAcquire::http::Retries "8";\nAcquire::http::Timeout "60";\n' \
        > /etc/apt/apt.conf.d/99net; \
      apt-get update -qq; \
      uri=$(apt-get install --print-uris -y --no-install-recommends fonts-noto-cjk \
        | sed -n "s/^'\([^']*\)'.*/\1/p" | head -1); \
      echo "font package: $uri"; \
      # apt's own fetch gives up on a ~50 MB transfer over a link that drops, and
      # it starts over each time. curl resumes, so a bad link costs seconds rather
      # than the whole build.
      for attempt in 1 2 3 4 5 6 7 8; do \
        if curl -fsSL -C - -o /tmp/fonts-noto-cjk.deb "$uri"; then break; fi; \
        echo "font download interrupted, retrying ($attempt)"; sleep 3; \
      done; \
      dpkg -i /tmp/fonts-noto-cjk.deb; \
      fc-cache -f >/dev/null 2>&1 || true; \
      rm -f /tmp/fonts-noto-cjk.deb; \
      rm -rf /var/lib/apt/lists/*; \
    fi

COPY bin/piforged /app/bin/piforged
COPY bin/fakepi /app/bin/fakepi
COPY web/.next /app/web/.next
COPY web/public /app/web/public
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
