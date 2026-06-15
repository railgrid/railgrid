# syntax=docker/dockerfile:1

# 1. Build the portal micro-frontend (Vite + TS → portal/dist).
FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

# 2. Build the Go binary. The binary serves `init` + `serve`, so the whole
#    module source has to be present. The kedge-provider-sdk is now a published
#    dependency (no replace), so `go mod download` fetches it from the proxy.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/infrastructure-provider .

# 3. Minimal runtime image.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/infrastructure-provider /infrastructure-provider
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/infrastructure-provider"]
