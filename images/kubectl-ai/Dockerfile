ARG GO_VERSION=1.24.3
ARG KUBECTL_VERSION=v1.33.0
ARG GCLOUD_CLI_VERSION=530.0.0-stable

FROM golang:${GO_VERSION}-bookworm as builder

WORKDIR /src
COPY go.mod go.sum ./
COPY gollm/ ./gollm/
RUN go mod download

COPY cmd/ ./cmd/
COPY pkg/ ./pkg/

RUN CGO_ENABLED=0 go build -o kubectl-ai ./cmd/
FROM debian:bookworm-slim AS kubectl-tool
RUN apt-get update && \
    apt-get install -y --no-install-recommends curl ca-certificates && \
    mkdir -p /opt/tools/kubectl/bin/ && \
    curl -L "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o /opt/tools/kubectl/bin/kubectl && \
    chmod +x /opt/tools/kubectl/bin/kubectl && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:${GCLOUD_CLI_VERSION} AS runtime
RUN apt-get update -y && \
    apt-get install -y apt-transport-https ca-certificates gnupg curl ca-certificates && \
    curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee /etc/apt/sources.list.d/google-cloud-sdk.list && \
    apt-get update -y && \
    apt-get install -y google-cloud-cli-gke-gcloud-auth-plugin && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /src/kubectl-ai /bin/kubectl-ai
COPY --from=kubectl-tool /opt/tools/kubectl/ /opt/tools/kubectl/
RUN ln -sf /opt/tools/kubectl/bin/kubectl /bin/kubectl

ENTRYPOINT [ "/bin/kubectl-ai" ]