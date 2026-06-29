# Sandbox runner for the infrastructure provider's `sandbox-runner` template.
# The runner is a self-contained, stdlib-only Go binary (its own go.mod, no
# requires), so the build context is just this dir — no cross-provider COPY.
# The node base is intentional: the runner supervises a user dev process that
# shells out to npm/vite, and git is needed for workspace operations.
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sandbox-runner .

FROM node:22-bookworm
RUN apt-get update && apt-get install -y ca-certificates git && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sandbox-runner /usr/local/bin/sandbox-runner
WORKDIR /workspace
EXPOSE 3000 7070
ENTRYPOINT ["/usr/local/bin/sandbox-runner"]
