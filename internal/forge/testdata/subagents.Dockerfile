# The pinned subagent runtime for the delegated-usage integration test
# (TestIntegrationDelegatedUsageOnTheWorker): apptown-agent's own Pi layer, with
# exactly the pi-subagents lines of deploy/apptown-agent/Dockerfile added, for a
# machine whose apptown-agent was built before that extension was baked.
# Build: docker build -t apptown-agent:s5-subagents-test -f internal/forge/testdata/subagents.Dockerfile internal/forge/testdata
FROM apptown-agent:latest
ARG PI_SUBAGENTS_VERSION=0.61.0
RUN HOME=/opt/pi-baked pi install npm:pi-subagents@${PI_SUBAGENTS_VERSION}
RUN f=/opt/pi-baked/.pi/agent/npm/node_modules/pi-subagents/src/runs/shared/model-fallback.ts && \
    sed -i 's|if (expectedBase === observedBase) return undefined;|if (expectedBase === observedBase) return undefined;\n\t/* forge-patch-dated-ids */ if (stripTrailingDateStamp(expectedBase.slice(expectedBase.lastIndexOf("/") + 1)) === stripTrailingDateStamp(observedBase.slice(observedBase.lastIndexOf("/") + 1))) return undefined;|' "$f" && \
    grep -q "forge-patch-dated-ids" "$f"
RUN cd /tmp && HOME=/opt/pi-baked PI_SKIP_VERSION_CHECK=1 \
      timeout 90 pi --session-dir /tmp/warm --session-id warm \
      --no-context-files --print warm-cache >/dev/null 2>&1 || true; \
    rm -rf /tmp/warm /tmp/pi-subagents-uid-0; test -d /tmp/jiti
LABEL forge.pi-subagents.version=${PI_SUBAGENTS_VERSION}
