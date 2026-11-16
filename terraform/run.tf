# Demo app on Cloud Run with two sidecars:
#   - PGAdapter: Spanner PostgreSQL interface on localhost:5433
#   - shadowcut shadow proxy: localhost:5434 -> Cloud SQL, shadowing reads to PGAdapter
# Cloud Run cannot expose raw TCP, so the proxy runs as a sidecar of the app.
resource "google_cloud_run_v2_service" "demoapp" {
  name     = "shadowcut-demoapp"
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"
  template {
    service_account = google_service_account.run.email
    scaling {
      min_instance_count = 1
      max_instance_count = 3
    }
    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [google_sql_database_instance.src.connection_name]
      }
    }
    containers {
      name       = "app"
      image      = local.image
      args       = ["sc-demoapp"]
      depends_on = ["pgadapter", "shadow"]
      ports {
        container_port = 8080
      }
      env {
        name  = "LISTEN"
        value = ":8080"
      }
      env {
        name  = "PG_DSN"
        value = "postgres://shadowcut@/wiki?host=/cloudsql/${google_sql_database_instance.src.connection_name}"
      }
      env {
        name  = "READ_DSN"
        value = "postgres://shadowcut@localhost:5434/wiki?sslmode=disable"
      }
      env {
        name  = "SPANNER_DSN"
        value = "postgres://localhost:5433/wiki?sslmode=disable"
      }
      env {
        name = "PGPASSWORD"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.pg_password.secret_id
            version = "latest"
          }
        }
      }
      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
    }
    containers {
      name  = "pgadapter"
      image = "gcr.io/cloud-spanner-pg-adapter/pgadapter:latest"
      args  = ["-p", var.project_id, "-i", google_spanner_instance.sc.name, "-d", google_spanner_database.wiki.name, "-s", "5433", "-x"]
      startup_probe {
        tcp_socket {
          port = 5433
        }
      }
    }
    containers {
      name  = "shadow"
      image = local.image
      args  = ["shadowcut", "shadow", "proxy", "--listen", ":5434", "--upstream", "/cloudsql/${google_sql_database_instance.src.connection_name}/.s.PGSQL.5432", "--out", "/tmp/shadow"]
      env {
        name  = "SPANNER_DSN"
        value = "postgres://localhost:5433/wiki?sslmode=disable"
      }
      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
      startup_probe {
        tcp_socket {
          port = 5434
        }
      }
    }
  }
}

# Experiment runner (Exp 3 hotspot benchmark, emulator confirmations) as a Cloud Run job.
resource "google_cloud_run_v2_job" "exp" {
  name     = "shadowcut-exp"
  location = var.region
  template {
    template {
      service_account = google_service_account.run.email
      timeout         = "3600s"
      containers {
        image = local.image
        args  = ["sc-exp", "exp3", "--dsn", "postgres://localhost:5433/wiki?sslmode=disable", "--out", "/tmp/exp3"]
      }
      containers {
        name  = "pgadapter"
        image = "gcr.io/cloud-spanner-pg-adapter/pgadapter:latest"
        args  = ["-p", var.project_id, "-i", google_spanner_instance.sc.name, "-d", google_spanner_database.wiki.name, "-s", "5433", "-x"]
      }
    }
  }
}
