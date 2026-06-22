# Frontend tier of the "application" 3-tier demo. Build context is this dir
# (self-contained module, standard library only — no go.sum).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/frontend .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/frontend /frontend
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/frontend"]
