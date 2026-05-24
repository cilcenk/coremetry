#!/bin/sh
set -e

echo "[start] WildFly + built-in MicroProfile OpenTelemetry (no javaagent)"

# Background CPU profiler — async-profiler via JVMTI. Pushes to
# Coremetry's /v1/profiles. No JVM agent involved at premain so
# it doesn't conflict with WildFly's logmanager bootstrap.
/profile-pusher.sh &

# Pivot from the OTel javaagent (v0.5.208 demo work): under Java
# 17's strict module system the agent's premain triggers JUL's
# LogManager.getLogManager() BEFORE WildFly's modular bootstrap
# installs org.jboss.logmanager.LogManager, and the resulting
# LogManager-not-properly-installed error stops the server from
# booting. Even -Xbootclasspath/a:jboss-logmanager.jar +
# -Djava.util.logging.manager= ... can't claw it back on Java
# 17+ because java.logging is a JPMS module.
#
# WildFly 34 ships its own MicroProfile OpenTelemetry subsystem
# in standalone-microprofile.xml — same OTLP exporter, no
# pre-main agent shenanigans. We point it at the collector via
# system properties (MicroProfile Config reads otel.* keys from
# system props automatically).
export JAVA_OPTS="${JAVA_OPTS:-} \
  -Dotel.sdk.disabled=false \
  -Dotel.service.name=${OTEL_SERVICE_NAME:-jboss-demo} \
  -Dotel.exporter.otlp.endpoint=${OTEL_EXPORTER_OTLP_ENDPOINT:-http://otel-collector:4317} \
  -Dotel.exporter.otlp.protocol=${OTEL_EXPORTER_OTLP_PROTOCOL:-grpc} \
  -Dotel.metrics.exporter=${OTEL_METRICS_EXPORTER:-otlp} \
  -Dotel.logs.exporter=${OTEL_LOGS_EXPORTER:-otlp} \
  -Dotel.traces.exporter=${OTEL_TRACES_EXPORTER:-otlp} \
  -Dotel.propagators=${OTEL_PROPAGATORS:-tracecontext,baggage} \
  -Dotel.resource.attributes=${OTEL_RESOURCE_ATTRIBUTES} \
  -Djboss.bind.address=0.0.0.0 \
  -Djboss.bind.address.management=0.0.0.0 \
"

# Bind WildFly to all interfaces; standalone-microprofile.xml has
# the OTel subsystem pre-wired and reads the otel.* sys props
# above on boot.
exec /opt/jboss/wildfly/bin/standalone.sh \
  -b 0.0.0.0 -bmanagement 0.0.0.0 \
  --server-config=standalone-microprofile.xml
