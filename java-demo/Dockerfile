# ── Stage 1: Maven build ─────────────────────────────────────────────────────
FROM maven:3.9-eclipse-temurin-25 AS builder
WORKDIR /build
COPY pom.xml .
RUN mvn -B dependency:go-offline
COPY src ./src
RUN mvn -B -DskipTests package && \
    cp target/*.jar /demo.jar

# ── Stage 2: runtime ─────────────────────────────────────────────────────────
FROM eclipse-temurin:25-jre-jammy

ARG TARGETARCH
ARG OTEL_AGENT_VERSION=2.10.0
ARG ASYNC_PROFILER_VERSION=3.0

# OpenTelemetry Java auto-instrumentation agent (zero-code traces/metrics/logs)
ADD https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v${OTEL_AGENT_VERSION}/opentelemetry-javaagent.jar /agent/opentelemetry-javaagent.jar

# Pyroscope Java agent — pushes continuous CPU + alloc profiles to Pyroscope
# server every ~10s. Uses async-profiler under the hood. Activated via
# the PYROSCOPE_* env vars wired in compose. Sourced from the official
# GitHub release (the Maven artifact name has shifted between versions).
ARG PYROSCOPE_AGENT_VERSION=0.14.0
ADD https://github.com/grafana/pyroscope-java/releases/download/v${PYROSCOPE_AGENT_VERSION}/pyroscope.jar /agent/pyroscope.jar

# async-profiler for CPU profiling (zero-code, attaches via JVMTI)
RUN apt-get update -qq && \
    apt-get install -y -qq --no-install-recommends curl ca-certificates && \
    case "$TARGETARCH" in \
      arm64) ARCH=arm64 ;; \
      *)     ARCH=x64   ;; \
    esac && \
    curl -fsSL "https://github.com/async-profiler/async-profiler/releases/download/v${ASYNC_PROFILER_VERSION}/async-profiler-${ASYNC_PROFILER_VERSION}-linux-${ARCH}.tar.gz" \
      -o /tmp/asprof.tgz && \
    mkdir -p /opt && tar -xzf /tmp/asprof.tgz -C /opt && \
    mv /opt/async-profiler-* /opt/async-profiler && \
    rm /tmp/asprof.tgz && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=builder /demo.jar /app/demo.jar
COPY scripts/start.sh /start.sh
COPY scripts/profile-pusher.sh /profile-pusher.sh
RUN chmod +x /start.sh /profile-pusher.sh

EXPOSE 8080
ENTRYPOINT ["/start.sh"]
