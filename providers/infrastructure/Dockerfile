# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist) in a node
#    stage. portal/ is a self-contained npm project so we only need its
#    package.json/lockfile + source — no host-side npm install required.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. assets.go embeds portal/dist via //go:embed, so
#    the dist/ output from the previous stage has to land at the right
#    relative path before `go build` runs.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go heartbeat.go assets.go ./
COPY server/ ./server/
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/infrastructure-provider .

# 3. Minimal runtime image. The portal assets are baked into the binary,
#    so there is nothing else to copy.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/infrastructure-provider /infrastructure-provider
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/infrastructure-provider"]
