# Nightly pg_dump to R2. This is the RPO: at most 24 hours of bookings lost on
# a total database loss. The restore drill lives in the README, run it before
# you need it.

resource "kubernetes_secret_v1" "backup" {
  metadata {
    name      = "carshare-backup"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }
  data = {
    DATABASE_URL          = local.effective_database_url
    AWS_ACCESS_KEY_ID     = var.r2_access_key_id
    AWS_SECRET_ACCESS_KEY = var.r2_secret_access_key
  }
}

resource "kubernetes_cron_job_v1" "backup" {
  metadata {
    name      = "carshare-backup"
    namespace = kubernetes_namespace_v1.carshare.metadata[0].name
  }

  spec {
    schedule                      = "15 3 * * *"
    concurrency_policy            = "Forbid"
    successful_jobs_history_limit = 3
    failed_jobs_history_limit     = 3

    job_template {
      metadata {}
      spec {
        backoff_limit = 2
        template {
          metadata {}
          spec {
            restart_policy = "Never"

            init_container {
              name  = "dump"
              image = "postgres:16"
              command = ["sh", "-c",
                "pg_dump \"$DATABASE_URL\" -Fc -f /backup/carshare.dump && ls -lh /backup"
              ]
              env {
                name = "DATABASE_URL"
                value_from {
                  secret_key_ref {
                    name = kubernetes_secret_v1.backup.metadata[0].name
                    key  = "DATABASE_URL"
                  }
                }
              }
              volume_mount {
                name       = "backup"
                mount_path = "/backup"
              }
            }

            container {
              name  = "upload"
              image = "amazon/aws-cli:2.22.35"
              command = ["sh", "-c",
                "aws s3 cp /backup/carshare.dump \"s3://${var.r2_bucket}/carshare/$(date -u +%Y-%m-%d).dump\" --endpoint-url \"${var.r2_endpoint}\""
              ]
              env_from {
                secret_ref {
                  name = kubernetes_secret_v1.backup.metadata[0].name
                }
              }
              volume_mount {
                name       = "backup"
                mount_path = "/backup"
              }
            }

            volume {
              name = "backup"
              empty_dir {}
            }
          }
        }
      }
    }
  }
}
