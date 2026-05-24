# ── Stage 1: Maven build ─────────────────────────────────────────────────────
FROM maven:3.9-eclipse-temurin-17 AS builder
WORKDIR /build
COPY pom.xml .
RUN mvn -B dependency:go-offline
COPY src ./src
RUN mvn -B -DskipTests package && \
    cp target/jboss-demo.war /jboss-demo.war

# ── Stage 2: WildFly runtime ─────────────────────────────────────────────────
# Upstream open-source JBoss — same module / standalone.xml model as
# JBoss EAP, no licensing for a demo. JDK 17 to match the OTel agent's
# supported runtimes; standalone-microprofile.xml gives us CDI + JAX-RS
# without paying for the full Jakarta EE platform extras.
FROM quay.io/wildfly/wildfly:34.0.0.Final-jdk17

ARG TARGETARCH
ARG OTEL_AGENT_VERSION=2.10.0
ARG PYROSCOPE_AGENT_VERSION=0.14.0
ARG ASYNC_PROFILER_VERSION=3.0

USER root

# OpenTelemetry Java auto-instrumentation agent. JBoss-aware out of the
# box — WildFly's Undertow, RESTEasy, CDI, and JDK HttpClient are all
# auto-traced by this single jar. Stamps logs with trace_id via the
# JUL appender mdc.
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v${OTEL_AGENT_VERSION}/opentelemetry-javaagent.jar /opt/agent/opentelemetry-javaagent.jar

# Pyroscope Java agent — pushes continuous JFR-format profiles to
# Pyroscope every ~10s. Native JFR API under the hood so it stacks
# cleanly with the JVM's own -XX:StartFlightRecording without fighting
# over the recording slot.
ADD https://github.com/grafana/pyroscope-java/releases/download/v${PYROSCOPE_AGENT_VERSION}/pyroscope.jar /opt/agent/pyroscope.jar

# async-profiler — used by the Coremetry profile-pusher sidecar to
# capture short windows + POST collapsed stacks to /v1/profiles.
# Independent of Pyroscope so the demo emits to both backends.
#
# UBI base ships curl-minimal + tar already; we skip installing curl
# (conflicts with curl-minimal) and only pull gzip because tar
# alone can't read .tar.gz on the minimal image.
RUN microdnf install -y gzip procps-ng && \
    case "$TARGETARCH" in \
      arm64) ARCH=arm64 ;; \
      *)     ARCH=x64   ;; \
    esac && \
    curl -fsSL "https://github.com/async-profiler/async-profiler/releases/download/v${ASYNC_PROFILER_VERSION}/async-profiler-${ASYNC_PROFILER_VERSION}-linux-${ARCH}.tar.gz" \
      -o /tmp/asprof.tgz && \
    mkdir -p /opt && tar -xzf /tmp/asprof.tgz -C /opt && \
    mv /opt/async-profiler-* /opt/async-profiler && \
    rm /tmp/asprof.tgz && \
    microdnf clean all

# Drop the WAR into WildFly's auto-deploy slot. The .dodeploy marker
# is the standalone deployment scanner's "go ahead" signal — without
# it WildFly inspects the WAR but waits for an explicit kick.
COPY --from=builder /jboss-demo.war /opt/jboss/wildfly/standalone/deployments/jboss-demo.war
RUN touch /opt/jboss/wildfly/standalone/deployments/jboss-demo.war.dodeploy && \
    mkdir -p /opt/jfr && \
    chown -R jboss:0 /opt/jboss/wildfly/standalone/deployments /opt/jfr /opt/agent /opt/async-profiler

COPY scripts/start.sh /start.sh
COPY scripts/profile-pusher.sh /profile-pusher.sh
RUN chmod +x /start.sh /profile-pusher.sh

USER jboss
EXPOSE 8080 9990

ENTRYPOINT ["/start.sh"]
