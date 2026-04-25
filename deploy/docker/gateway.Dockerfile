FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o /out/gateway ./services/gateway/cmd/gateway

FROM alpine:3.21
WORKDIR /app
COPY --from=build /out/gateway /usr/local/bin/gateway
EXPOSE 4010
CMD ["gateway"]
