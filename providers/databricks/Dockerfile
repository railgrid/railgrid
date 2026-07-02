# syntax=docker/dockerfile:1

FROM node:22-alpine AS portal
WORKDIR /portal
COPY portal/package.json portal/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY portal/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=portal /portal/dist ./portal/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/databricks-provider .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/databricks-provider /databricks-provider
COPY deploy/chart/files/schemas /etc/kedge/schemas
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/databricks-provider"]
