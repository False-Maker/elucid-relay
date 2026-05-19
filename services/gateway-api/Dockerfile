FROM golang:1.23-alpine AS build

WORKDIR /src

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

COPY services/gateway-api/go.mod services/gateway-api/go.sum* ./
RUN go mod download

COPY services/gateway-api/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/gateway-api ./cmd/gateway-api

FROM alpine:3.20

RUN adduser -D -H relay

COPY --from=build /out/gateway-api /usr/local/bin/gateway-api

USER relay
EXPOSE 8080

ENTRYPOINT ["gateway-api"]
CMD ["serve"]
