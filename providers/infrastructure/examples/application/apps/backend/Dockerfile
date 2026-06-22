# Backend tier of the "application" 3-tier demo. Build context is this dir
# (the module is self-contained — its own go.mod).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/backend .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/backend /backend
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/backend"]
