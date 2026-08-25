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

# The API's own hostname when a frontend worker owns the main domain.
resource "cloudflare_record" "api_origin" {
  count   = local.api_domain == var.domain ? 0 : 1
  zone_id = var.cloudflare_zone_id
  name    = local.api_domain
  type    = "CNAME"
  content = var.ingress_target
  proxied = true
}

# Routes every request for the main domain to the frontend worker, which
# serves the site and proxies /v1 back to the API hostname above.
resource "cloudflare_worker_route" "site" {
  count       = var.worker_script == "" ? 0 : 1
  zone_id     = var.cloudflare_zone_id
  pattern     = "${var.domain}/*"
  script_name = var.worker_script
}

resource "cloudflare_healthcheck" "api" {
  count   = var.enable_healthcheck ? 1 : 0
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
