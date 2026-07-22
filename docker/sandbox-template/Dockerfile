FROM docker/sandbox-templates:codex-docker

LABEL org.opencontainers.image.title="Factory Codex sandbox"
LABEL org.opencontainers.image.description="Rust development environment for Factory in Docker Sandboxes"

USER root

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        build-essential \
        ca-certificates \
        libssl-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

USER agent

ARG RUST_TOOLCHAIN=1.96.0
ENV CARGO_HOME=/home/agent/.cargo
ENV RUSTUP_HOME=/home/agent/.rustup
ENV PATH=/home/agent/.cargo/bin:${PATH}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN curl --proto '=https' --tlsv1.2 --fail --silent --show-error \
        https://sh.rustup.rs \
        | sh -s -- -y --profile minimal --default-toolchain "${RUST_TOOLCHAIN}" \
    && rustup component add clippy rustfmt \
    && cargo --version \
    && rustc --version
