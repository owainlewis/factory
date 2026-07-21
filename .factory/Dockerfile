FROM rust:1.88-bookworm

ARG CODEX_VERSION=0.145.0
ARG GH_VERSION=2.94.0

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        ca-certificates \
        curl \
        git \
        nodejs \
        npm \
        openssh-client \
        pkg-config \
        libssl-dev \
    && npm install --global "@openai/codex@${CODEX_VERSION}" \
    && architecture="$(dpkg --print-architecture)" \
    && case "$architecture" in amd64) gh_architecture=amd64 ;; arm64) gh_architecture=arm64 ;; *) exit 1 ;; esac \
    && curl --fail --silent --show-error --location \
        "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${gh_architecture}.tar.gz" \
        --output /tmp/gh.tar.gz \
    && tar --extract --gzip --file /tmp/gh.tar.gz --directory /tmp \
    && install "/tmp/gh_${GH_VERSION}_linux_${gh_architecture}/bin/gh" /usr/local/bin/gh \
    && find /usr/local/cargo/bin -maxdepth 1 \( -type f -o -type l \) \
        -exec ln -s {} /usr/local/bin/ \; \
    && rm -rf /var/lib/apt/lists/* /root/.npm /tmp/gh.tar.gz /tmp/gh_*

RUN groupadd --gid 1000 agent \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash agent

USER 1000:1000
WORKDIR /workspace
