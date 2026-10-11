# Judge image for local development and CI. Production runs the binary on
# the host under systemd instead (deploy/systemd/README.md): isolate needs a
# delegated cgroup, which only a privileged container can provide.

ARG GO_IMAGE=golang:1.24.1-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
ARG ISOLATE_VERSION=v2.7

# --- isolate ----------------------------------------------------------------
FROM ${RUNTIME_IMAGE} AS isolate
ARG ISOLATE_VERSION
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc libc6-dev make libcap-dev libseccomp-dev pkg-config git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "${ISOLATE_VERSION}" https://github.com/ioi/isolate.git /src/isolate \
 && make -C /src/isolate isolate

# --- Go binaries --------------------------------------------------------------
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /out/verdale-judge . \
 && CGO_ENABLED=0 go build -o /out/mockcore ./cmd/mockcore \
 && CGO_ENABLED=0 go build -o /out/submit ./cmd/submit

# --- Toolchains every sandbox sees (pinned by languages.*.expected_version) ---
FROM ${RUNTIME_IMAGE} AS toolchains
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc g++ libc6-dev python3 libcap2 libseccomp2 ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=isolate /src/isolate/isolate /usr/local/bin/isolate
COPY deploy/docker/isolate.cf /usr/local/etc/isolate
COPY deploy/docker/entrypoint.sh /usr/local/bin/judge-entrypoint
RUN chmod 0755 /usr/local/bin/judge-entrypoint \
 && mkdir -p /var/local/lib/isolate /var/lib/verdale-judge/storage /var/lib/verdale-judge/cache /var/lib/verdale-judge/work
ENTRYPOINT ["judge-entrypoint"]

# --- Integration tests: real isolate, real Redis (docker-compose.test.yml) ---
FROM toolchains AS integration
COPY --from=build /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["go", "test", "-count=1", "-tags", "integration", "-v", "./integration/..."]

# --- Judge (default target) ----------------------------------------------------
FROM toolchains AS judge
COPY --from=build /out/ /usr/local/bin/
COPY config.json /etc/verdale-judge/config.json
ENV VERDALE_JUDGE_CONFIG=/etc/verdale-judge/config.json
EXPOSE 8081
CMD ["verdale-judge"]
