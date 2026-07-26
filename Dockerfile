# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/lazyrepo ./cmd/lazyrepo

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/lazyrepo /lazyrepo
EXPOSE 8080
ENTRYPOINT ["/lazyrepo"]
CMD ["-config", "/data/config.yaml"]
