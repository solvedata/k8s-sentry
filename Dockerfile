FROM golang:1.14-alpine AS builder

WORKDIR /go/src/app
COPY . .
RUN go build

FROM alpine:3.11
COPY --from=builder /go/src/app/k8s-sentry /

ENTRYPOINT ["/k8s-sentry"]
