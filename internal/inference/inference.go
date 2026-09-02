package inference

import (
	"strconv"
	"strings"

	"defenseunicorns/uds-compose-bridge/internal/model"
)

var wellKnownMetricsPorts = map[int]struct{}{
	9090: {}, // Prometheus
	9100: {}, // Prometheus Node Exporter
	9115: {}, // Prometheus Blackbox Exporter
	9121: {}, // Prometheus Redis Exporter
	9153: {}, // CoreDNS metrics
	9187: {}, // Prometheus PostgreSQL Exporter
	9256: {}, // Prometheus Process Exporter
}

// MetricsPorts returns the service ports that imply generated monitor entries.
func MetricsPorts(service model.Service) []model.Port {
	environmentPorts := metricsEnvironmentPorts(service.Env)
	ports := make([]model.Port, 0, len(service.Ports))
	for _, port := range service.Ports {
		if !strings.EqualFold(port.Protocol, "TCP") {
			continue
		}
		_, wellKnown := wellKnownMetricsPorts[port.Number]
		_, configuredByEnvironment := environmentPorts[port.Number]
		if isMetricsPortName(port.Name) || wellKnown || configuredByEnvironment {
			ports = append(ports, port)
		}
	}
	return ports
}

// PrimaryExposedService returns the host and service used for inferred SSO.
func PrimaryExposedService(app model.App) (host string, service string) {
	if app.Package.NetworkExposeConfigured {
		for _, raw := range app.Package.NetworkExpose {
			if item, ok := raw.(map[string]any); ok {
				host, _ = item["host"].(string)
				service, _ = item["service"].(string)
				if host == "" {
					host = service
				}
				return host, service
			}
		}
		return "", ""
	}
	for _, candidate := range app.Services {
		for _, port := range candidate.Ports {
			if port.Published {
				return candidate.Name, candidate.Name
			}
		}
	}
	return "", ""
}

func isMetricsPortName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	for _, token := range strings.Split(normalized, "-") {
		if token == "metrics" || token == "prometheus" {
			return true
		}
	}
	return false
}

func metricsEnvironmentPorts(environment []model.EnvVar) map[int]struct{} {
	ports := map[int]struct{}{}
	for _, item := range environment {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		if name != "METRICS_PORT" && name != "PROMETHEUS_PORT" {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err == nil && port > 0 && port <= 65535 {
			ports[port] = struct{}{}
		}
	}
	return ports
}
