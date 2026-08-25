# DNS plus an external health check. The health check is the black-box half of
# the SLO measurement: it sees what users see, including DNS, TLS, and the
# load balancer, which in-cluster metrics never can.

resource "cloudflare_record" "api" {
  zone_id = var.cloudflare_zone_id
  name    = var.domain
  type    = "CNAME"
  content = var.ingress_target
  proxied = true
}

resource "cloudflare_healthcheck" "api" {
  zone_id = var.cloudflare_zone_id
  name    = "carshare-api"
  address = var.domain
  type    = "HTTPS"
  path    = "/health"

  check_regions = ["WNAM", "ENAM", "WEU"]

  interval              = 60
  retries               = 2
  timeout               = 5
  consecutive_fails     = 2
  consecutive_successes = 2
}
