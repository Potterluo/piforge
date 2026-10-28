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

COPY bin/piforged /app/bin/piforged
COPY bin/fakepi /app/bin/fakepi
COPY web/.next /app/web/.next
COPY web/public /app/web/public
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh
