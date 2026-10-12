ARG GO_IMAGE
ARG NODE_IMAGE
ARG PYTHON_IMAGE
ARG RUST_IMAGE

FROM ${GO_IMAGE} AS go_toolchain
FROM ${NODE_IMAGE} AS node_toolchain
FROM ${PYTHON_IMAGE} AS python_toolchain
FROM ${RUST_IMAGE}

COPY --from=go_toolchain /usr/local/go /opt/toolchains/go
COPY --from=node_toolchain /usr/local/bin/node /opt/toolchains/node/bin/node
COPY --from=node_toolchain /usr/local/lib/node_modules/npm /opt/toolchains/node/lib/node_modules/npm
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /opt/toolchains/node/bin/npm
COPY --from=python_toolchain /usr/local /opt/toolchains/python/usr-local
COPY standard-code-runner /traverse-board/standard-code-runner

USER 65532:65532
WORKDIR /workspace
ENTRYPOINT ["/traverse-board/standard-code-runner"]
