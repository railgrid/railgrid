# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/quickstart-provider .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/quickstart-provider /quickstart-provider
EXPOSE 8081
ENV PORT=8081
USER nonroot:nonroot
ENTRYPOINT ["/quickstart-provider"]
