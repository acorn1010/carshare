# The service itself: namespace, secrets, deployment, service, ingress, HPA,
# PDB. Rolling deploys are gated by the readiness probe and the PDB, so a bad
# image never takes the old pods down with it.

resource "kubernetes_namespace_v1" "carshare" {
  metadata {
    name = var.namespace
  }
}

resource "kubernetes_secret_v1" "carshare" {
  metadata {
    name      = "carshare"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  data = {
    DATABASE_URL         = local.effective_database_url
    POSTGRES_PASSWORD    = var.postgres_password
    GOOGLE_CLIENT_ID     = var.google_client_id
    GOOGLE_CLIENT_SECRET = var.google_client_secret
  }
}

resource "kubernetes_deployment_v1" "carshare" {
  metadata {
    name      = "carshare"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
    labels    = { app = "carshare" }
  }

  spec {
    selector {
      match_labels = { app = "carshare" }
    }

    template {
      metadata {
        labels = { app = "carshare" }
        annotations = {
          "prometheus.io/scrape" = "true"
          "prometheus.io/path"   = "/metrics"
          "prometheus.io/port"   = "9090"
        }
      }

      spec {
        dynamic "image_pull_secrets" {
          for_each = var.image_pull_secret == "" ? [] : [var.image_pull_secret]
          content {
            name = image_pull_secrets.value
          }
        }
        container {
          name  = "carshare"
          image = "${var.image}:${var.image_tag}"

          port {
            name           = "http"
            container_port = 3000
          }
          port {
            name           = "metrics"
            container_port = 9090
          }

          env {
            name  = "OAUTH_REDIRECT_URL"
            value = "https://${var.domain}/v1/auth/google/callback"
          }
          env_from {
            secret_ref {
              name = kubernetes_secret_v1.carshare.metadata[0].name
            }
          }

          resources {
            requests = {
              cpu    = "100m"
              memory = "64Mi"
            }
            limits = {
              memory = "256Mi"
            }
          }

          readiness_probe {
            http_get {
              path = "/health"
              port = 3000
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }
          liveness_probe {
            http_get {
              path = "/health"
              port = 3000
            }
            initial_delay_seconds = 15
            period_seconds        = 20
          }
        }
      }
    }
  }

  lifecycle {
    # The HPA owns the replica count.
    ignore_changes = [spec[0].replicas]
  }
}

resource "kubernetes_service_v1" "carshare" {
  metadata {
    name      = "carshare"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  spec {
    selector = { app = "carshare" }
    port {
      name        = "http"
      port        = 80
      target_port = "http"
    }
  }
}

resource "kubernetes_ingress_v1" "carshare" {
  metadata {
    name      = "carshare"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
    annotations = {
      "cert-manager.io/cluster-issuer" = "letsencrypt-prod"
      "kubernetes.io/tls-acme"         = "true"
    }
  }
  spec {
    ingress_class_name = "traefik"
    tls {
      hosts       = [var.domain]
      secret_name = "${var.domain}-tls"
    }
    rule {
      host = var.domain
      http {
        path {
          path      = "/"
          path_type = "Prefix"
          backend {
            service {
              name = kubernetes_service_v1.carshare.metadata[0].name
              port {
                name = "http"
              }
            }
          }
        }
      }
    }
  }
}

resource "kubernetes_horizontal_pod_autoscaler_v2" "carshare" {
  metadata {
    name      = "carshare"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  spec {
    min_replicas = var.min_replicas
    max_replicas = var.max_replicas
    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment_v1.carshare.metadata[0].name
    }
    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = 60
        }
      }
    }
  }
}

resource "kubernetes_pod_disruption_budget_v1" "carshare" {
  metadata {
    name      = "carshare"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  spec {
    min_available = 1
    selector {
      match_labels = { app = "carshare" }
    }
  }
}
