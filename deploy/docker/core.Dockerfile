FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o /out/core ./services/core/cmd/core

FROM alpine:3.21
WORKDIR /app
COPY --from=build /out/core /usr/local/bin/core
EXPOSE 4012
CMD ["core"]
