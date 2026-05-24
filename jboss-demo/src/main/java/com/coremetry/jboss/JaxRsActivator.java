package com.coremetry.jboss;

import jakarta.ws.rs.ApplicationPath;
import jakarta.ws.rs.core.Application;

// Mounts all JAX-RS resources under /api on the deployed WAR's
// context root. WildFly auto-scans the WAR for @Path classes, no
// explicit getClasses() override needed.
@ApplicationPath("/api")
public class JaxRsActivator extends Application {
}
