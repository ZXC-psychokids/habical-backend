FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o /out/auth ./services/auth/cmd/auth

FROM alpine:3.21
WORKDIR /app
COPY --from=build /out/auth /usr/local/bin/auth
EXPOSE 4011
CMD ["auth"]
