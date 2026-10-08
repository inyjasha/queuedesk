FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /queuedesk ./cmd/api

FROM build AS test
RUN apk add --no-cache gcc musl-dev
CMD ["go", "test", "-race", "-count=1", "-v", "./..."]

FROM alpine:3.22
RUN adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /queuedesk ./queuedesk
COPY docs/openapi.yaml ./docs/openapi.yaml
USER app
EXPOSE 8080
CMD ["./queuedesk"]
