# BASE_REGISTRY permite trocar a origem da imagem de build sem editar o
# Dockerfile. O default é o Docker Hub (usado pela CI e pelo build local).
ARG BASE_REGISTRY=docker.io/library

# Stage 1: build. As dependências são baixadas numa camada própria, que só é
# refeita quando go.mod/go.sum mudam.
FROM ${BASE_REGISTRY}/golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY *.go ./
# -trimpath e -s -w: binário menor e sem caminhos da máquina de build.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/evaluation-service .

# Stage 2: runtime distroless — sem shell, sem gerenciador de pacotes, só o
# binário e os certificados de CA. Menos pacotes = menos CVEs no scan do
# Trivy e menos ferramentas para um atacante usar dentro do container.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /out/evaluation-service /evaluation-service
# USER declarado na própria imagem (sugestão da correção da Fase 2): o
# container não roda como root nem fora do Kubernetes.
USER nonroot:nonroot
EXPOSE 8004
ENTRYPOINT ["/evaluation-service"]
