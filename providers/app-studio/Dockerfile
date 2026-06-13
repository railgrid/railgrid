# syntax=docker/dockerfile:1

# 1. Build the App Studio portal bundle. The actual provider UI source lives
# in providers/projects/portal/src; this wrapper package imports that source
# and emits the bundle under /ui/providers/app-studio/ so the hub proxy can
# strip the prefix and still load the same files.
FROM node:22-alpine AS portal
WORKDIR /portal
COPY providers/app-studio/portal/package.json providers/app-studio/portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY providers/app-studio/portal/ ./
COPY providers/projects/portal/src /projects/portal/src
RUN npm run build

# 2. Build the Go provider binary. assets.go embeds portal/dist via
# //go:embed, so the bundle from the previous stage must land at the same
# relative path before `go build` runs.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY providers/app-studio/ ./providers/app-studio/
COPY --from=portal /portal/dist ./providers/app-studio/portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app-studio-provider ./providers/app-studio

# 3. Minimal runtime image. The portal bundle is baked into the binary.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/app-studio-provider /app-studio-provider
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/app-studio-provider"]
