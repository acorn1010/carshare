# Alert rules, picked up by kube-prometheus-stack. Severity "critical" means
# someone gets paged, "warning" means a ticket. Alertmanager routing is cluster
# infrastructure and lives outside this repo: alerts are useless until it has
# receivers, see the README's monitoring section.

resource "kubernetes_manifest" "alerts" {
  count = var.enable_prometheus_rule ? 1 : 0
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata = {
      name      = "carshare"
      namespace = kubernetes_namespace_v1.carshare.metadata[0].name
    }
    spec = {
      groups = [
        {
          name = "carshare"
          rules = [
            {
              alert  = "CarshareDown"
              expr   = "max(up{k8s_app=\"carshare\"}) == 0 or absent(up{k8s_app=\"carshare\"})"
              for    = "5m"
              labels = { severity = "critical" }
              annotations = {
                summary = "No carshare pod is being scraped. Users cannot book."
              }
            },
            {
              alert  = "CarshareErrorRateHigh"
              expr   = "sum(rate(carshare_requests_total{status=\"5xx\"}[5m])) / clamp_min(sum(rate(carshare_requests_total[5m])), 1e-9) > 0.01"
              for    = "10m"
              labels = { severity = "warning" }
              annotations = {
                summary = "More than 1% of requests are failing with 5xx."
              }
            },
            {
              alert  = "CarshareErrorRateCritical"
              expr   = "sum(rate(carshare_requests_total{status=\"5xx\"}[5m])) / clamp_min(sum(rate(carshare_requests_total[5m])), 1e-9) > 0.05"
              for    = "5m"
              labels = { severity = "critical" }
              annotations = {
                summary = "More than 5% of requests are failing with 5xx. Booking is effectively down."
              }
            },
            {
              alert  = "CarshareLatencyHigh"
              expr   = "histogram_quantile(0.95, sum by (le) (rate(carshare_request_duration_seconds_bucket[5m]))) > 0.3"
              for    = "10m"
              labels = { severity = "warning" }
              annotations = {
                summary = "p95 latency is above 300ms, the SLO threshold."
              }
            },
            {
              alert  = "CarshareDoubleBookingDetected"
              expr   = "max(carshare_double_booked_pairs) > 0"
              for    = "0m"
              labels = { severity = "critical" }
              annotations = {
                summary = "Two confirmed reservations overlap on the same car. The exclusion constraint makes this impossible, so either the constraint was dropped or the invariant query changed. Page whoever touched the schema."
              }
            },
            {
              alert  = "CarshareDBPoolSaturated"
              expr   = "rate(carshare_db_pool_empty_acquires_total[5m]) > 1"
              for    = "5m"
              labels = { severity = "warning" }
              annotations = {
                summary = "Requests are queueing for database connections. Raise the pool size or find the slow query before this becomes latency."
              }
            },
            {
              alert  = "CarshareBackupStale"
              expr   = "time() - max(kube_job_status_completion_time{namespace=\"${var.namespace}\", job_name=~\"carshare-backup.*\"}) > 26 * 3600"
              for    = "0m"
              labels = { severity = "critical" }
              annotations = {
                summary = "No successful database backup in over 26 hours. The RPO promise is broken, fix with: kubectl -n ${var.namespace} create job --from=cronjob/carshare-backup carshare-backup-manual"
              }
            },
            {
              alert  = "CarshareHPAMaxed"
              expr   = "kube_horizontalpodautoscaler_status_current_replicas{namespace=\"${var.namespace}\", horizontalpodautoscaler=\"carshare\"} >= kube_horizontalpodautoscaler_spec_max_replicas{namespace=\"${var.namespace}\", horizontalpodautoscaler=\"carshare\"}"
              for    = "15m"
              labels = { severity = "warning" }
              annotations = {
                summary = "The autoscaler has been pinned at max replicas for 15 minutes. Raise max_replicas or find what is eating CPU."
              }
            },
          ]
        },
      ]
    }
  }
}
