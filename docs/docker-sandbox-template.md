# Develop Factory in a Docker Sandbox

Factory includes a custom Docker Sandbox template with the Rust toolchain and
native build dependencies needed to format, lint, build, and test the project.
It extends Docker's `codex-docker` template, so Codex and a private Docker
daemon are available inside the sandbox.

This template is separate from the sandbox template Factory's own workers use
when `worker.sandbox = "docker_sandbox"` (configured with `worker.template`,
e.g. `docker/sandbox-templates:codex-docker`); this template is only the
development environment used to work on Factory itself with `sbx`.

## Build and load the template

Docker Sandboxes has a separate image store from the host Docker daemon. Build
the image, export it, and load it into `sbx`:

```sh
docker build \
  --file docker/sandbox-template/Dockerfile \
  --tag factory-sandbox:rust-1.96 \
  docker/sandbox-template

docker image save factory-sandbox:rust-1.96 --output /tmp/factory-sandbox.tar
sbx template load /tmp/factory-sandbox.tar
```

The template pins the current development toolchain. To test another toolchain
without editing the template, pass a build argument:

```sh
docker build \
  --build-arg RUST_TOOLCHAIN=stable \
  --file docker/sandbox-template/Dockerfile \
  --tag factory-sandbox:rust-stable \
  docker/sandbox-template
```

## Start Codex

Use clone mode so Codex gets a complete Git repository and can create branches,
commit, and push. Run this command from the repository's main checkout. Docker
Sandboxes rejects clone mode from a linked Git worktree because its read-only
source mount cannot resolve the worktree's `.git` pointer.

```sh
sbx run \
  --clone \
  --name factory-dev \
  --template factory-sandbox:rust-1.96 \
  codex .
```

The agent name must remain `codex` because the template extends Docker's Codex
base image. Do not add credentials to the Dockerfile or saved template. Manage
them through `sbx secret set` so Docker injects them at runtime.

## Verify the environment

Run the repository checks from Codex or from another terminal:

```sh
sbx exec factory-dev cargo fmt --all --check
sbx exec factory-dev cargo clippy --locked --all-targets -- -D warnings
sbx exec factory-dev cargo test --locked --all-targets
sbx exec factory-dev docker version
```

Remove the sandbox when the work is complete:

```sh
sbx rm factory-dev
```

After changing the Dockerfile, rebuild and reload its tag. Docker Sandboxes
caches templates until their tag is replaced or `sbx reset` clears the cache.
