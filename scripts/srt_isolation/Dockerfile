# Disposable prerequisite experiment. Never used by TeamX serve or packaging.
FROM node:22-bookworm-slim@sha256:170fab707454b1350981adcdc2f536ec21f24d06d7f008e8d40064afeacb8a94
RUN apt-get -o Acquire::Retries=3 update && apt-get -o Acquire::Retries=3 install -y --no-install-recommends \
    bubblewrap socat ripgrep ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY node /usr/local/bin/node
WORKDIR /opt/srt
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force
WORKDIR /opt/dsh
COPY dsh/package.json dsh/package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY preflight.mjs /opt/experiment/preflight.mjs
RUN mkdir -p /lab && chown 10001:10001 /lab
USER 10001:10001
WORKDIR /lab
ENTRYPOINT ["node", "/opt/experiment/preflight.mjs"]
