# Feasibility prototype only; intentionally replaces the image's single-screen entrypoint.
FROM enterprise-public-cn-beijing.cr.volces.com/vefaas-public/aio-computer@sha256:5c5a6949718f984cf60fec02d19436a0738d81d113e782f366a03936640f5f1d
USER root
RUN groupadd -g 2100 screens \
 && useradd -u 2101 -g screens -d /var/lib/teamx-computer/A -s /bin/bash screen-a \
 && useradd -u 2102 -g screens -d /var/lib/teamx-computer/B -s /bin/bash screen-b \
 && mkdir -p /tmp/.X11-unix /tmp/.ICE-unix /var/lib/teamx-computer /workspace/agents /workspace/shared \
 && chmod 1777 /tmp/.X11-unix /tmp/.ICE-unix \
 && chown root:screens /workspace/shared && chmod 2770 /workspace/shared
COPY prototype/ /opt/teamx-computer-prototype/
COPY tools.lock.json /opt/tools.lock.json
HEALTHCHECK NONE
ENTRYPOINT ["python3", "/opt/teamx-computer-prototype/screens.py", "serve"]
