FROM golang:1.27-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /app/bin/reflex main.go

FROM alpine:3.20

# kubectl is required at runtime: sandboxed Wasm runbooks request cluster
# mutations through the env.kubectl_exec host function, which execs it after
# an OPA policy check. Without this binary every remediation is denied.
ARG KUBECTL_VERSION=v1.35.5
RUN apk add --no-cache ca-certificates curl \
 && curl -fsSL -o /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
 && chmod +x /usr/local/bin/kubectl \
 && kubectl version --client=true

WORKDIR /app

COPY --from=builder /app/bin/reflex .
# The Go toolchain is needed by the JIT coder agent, which shells out to
# `go build` to compile LLM-generated runbooks to wasip1.
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

# runbooks.yaml maps domains to DAGs; policy.rego is the authorization policy
# loaded at startup. Both are resolved relative to the working directory, so
# omitting policy.rego makes every kubectl_exec call fail closed.
COPY runbooks.yaml policy.rego ./
COPY scripts ./scripts
COPY wasm-runbooks ./wasm-runbooks

EXPOSE 8080 9090

CMD ["./reflex"]
