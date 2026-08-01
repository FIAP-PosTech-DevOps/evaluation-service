# BASE_REGISTRY permite trocar a origem das imagens base sem editar o
# Dockerfile. O default é o Docker Hub, para o build local continuar
# funcionando; o build-and-push.sh sobrescreve com o ECR privado espelhado.
ARG BASE_REGISTRY=docker.io/library

# Stage 1: Build
FROM ${BASE_REGISTRY}/golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -o evaluation-service .

# Stage 2: Runtime
ARG BASE_REGISTRY=docker.io/library
FROM ${BASE_REGISTRY}/alpine:3.19
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/evaluation-service .
EXPOSE 8004
CMD ["./evaluation-service"]
