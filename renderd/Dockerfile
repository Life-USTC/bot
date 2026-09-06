# Keep the compiler version aligned with the locked dependency graph and CI.
FROM rust:1.89-bookworm@sha256:948f9b08a66e7fe01b03a98ef1c7568292e07ec2e4fe90d88c07bb14563c84ff AS build

WORKDIR /src

# Keep the dependency compilation layer stable when only renderer sources
# change. The real source is copied and built in the following layer.
COPY Cargo.toml Cargo.lock ./
RUN mkdir -p src && printf 'fn main() {}\n' >src/main.rs \
    && cargo build --release --locked

COPY src ./src
COPY assets ./assets
# COPY preserves timestamps in some builders; touch the entrypoint so Cargo
# cannot mistake the dependency-cache dummy for the real renderer.
RUN touch src/main.rs \
    && cargo build --release --locked \
    && strip target/release/renderd

FROM debian:bookworm-slim

RUN sed -i 's|deb.debian.org|mirrors.ustc.edu.cn|g; s|security.debian.org|mirrors.ustc.edu.cn|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl fonts-firacode fonts-noto-cjk fonts-noto-cjk-extra \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 renderd \
    && useradd --system --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin renderd

COPY --from=build /src/target/release/renderd /usr/local/bin/renderd

USER 10001:10001
ENV RENDERD_ADDR=0.0.0.0:9123
EXPOSE 9123
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD curl --fail --silent http://127.0.0.1:9123/healthz
ENTRYPOINT ["renderd"]
