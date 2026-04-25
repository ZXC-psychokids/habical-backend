FROM golang:1.25-alpine AS build
WORKDIR /app
COPY . .
RUN go build -o /out/social ./services/social/cmd/social

FROM alpine:3.21
WORKDIR /app
COPY --from=build /out/social /usr/local/bin/social
EXPOSE 4013
CMD ["social"]
