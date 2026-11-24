# Disposable Linux route experiment, never a production worker image.
FROM node:22-bookworm-slim@sha256:170fab707454b1350981adcdc2f536ec21f24d06d7f008e8d40064afeacb8a94
RUN apt-get update && apt-get install -y --no-install-recommends python3 ca-certificates git curl procps \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/dsh
COPY node /usr/local/bin/node
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY launch.py probe.py outer-policy.json /opt/experiment/
COPY glab lark-cli /usr/local/bin/
COPY acceptance.test /opt/experiment/
WORKDIR /lab
ENTRYPOINT ["/opt/experiment/acceptance.test", "-test.run=^TestLinuxSpaceIsolationRoutes$", "-test.v"]
