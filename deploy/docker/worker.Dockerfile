FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o /out/worker ./services/worker/cmd/worker

FROM alpine:3.21
WORKDIR /app
COPY --from=build /out/worker /usr/local/bin/worker
CMD ["worker"]
