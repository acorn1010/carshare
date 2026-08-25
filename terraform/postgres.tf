# A single-pod Postgres inside the namespace, created only when no external
# database_url is given. Right-sized for a demo deployment and fully isolated:
# nothing outside this namespace can be touched by it. For real load, pass
# database_url to a managed Postgres instead and none of this exists.

locals {
  use_local_postgres = var.database_url == ""
  effective_database_url = local.use_local_postgres ? (
    "postgresql://postgres:${var.postgres_password}@carshare-postgres:5432/carshare?sslmode=disable"
  ) : var.database_url
}

resource "kubernetes_persistent_volume_claim_v1" "postgres" {
  count = local.use_local_postgres ? 1 : 0
  metadata {
    name      = "carshare-postgres"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  spec {
    access_modes = ["ReadWriteOnce"]
    resources {
      requests = {
        storage = "${var.postgres_storage_gi}Gi"
      }
    }
  }
  wait_until_bound = false
}

resource "kubernetes_deployment_v1" "postgres" {
  count = local.use_local_postgres ? 1 : 0
  metadata {
    name      = "carshare-postgres"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
    labels    = { app = "carshare-postgres" }
  }
  spec {
    replicas = 1
    strategy {
      # A PVC can only mount on one pod, so replace instead of rolling.
      type = "Recreate"
    }
    selector {
      match_labels = { app = "carshare-postgres" }
    }
    template {
      metadata {
        labels = { app = "carshare-postgres" }
      }
      spec {
        container {
          name  = "postgres"
          image = "postgres:16"
          args  = ["-c", "shared_buffers=512MB", "-c", "effective_cache_size=1GB"]
          env {
            name = "POSTGRES_PASSWORD"
            value_from {
              secret_key_ref {
                name = kubernetes_secret_v1.carshare.metadata[0].name
                key  = "POSTGRES_PASSWORD"
              }
            }
          }
          env {
            name  = "POSTGRES_DB"
            value = "carshare"
          }
          env {
            name  = "PGDATA"
            value = "/var/lib/postgresql/data/pgdata"
          }
          port {
            container_port = 5432
          }
          resources {
            requests = {
              cpu    = "250m"
              memory = "512Mi"
            }
            limits = {
              memory = "1536Mi"
            }
          }
          readiness_probe {
            exec {
              command = ["pg_isready", "-U", "postgres"]
            }
            initial_delay_seconds = 5
            period_seconds        = 10
          }
          volume_mount {
            name       = "data"
            mount_path = "/var/lib/postgresql/data"
          }
        }
        volume {
          name = "data"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim_v1.postgres[0].metadata[0].name
          }
        }
      }
    }
  }
}

resource "kubernetes_service_v1" "postgres" {
  count = local.use_local_postgres ? 1 : 0
  metadata {
    name      = "carshare-postgres"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  spec {
    selector = { app = "carshare-postgres" }
    port {
      port        = 5432
      target_port = 5432
    }
  }
}
