# Shadowcut cloud environment for the PENDING real-Spanner experiments.
# WRITTEN BUT NOT APPLIED (project rule: no cloud resources were created).
# See README "Pending cloud experiments" for the commands and cost estimates.

terraform {
  required_version = ">= 1.6"
  required_providers {
    google = { source = "hashicorp/google", version = ">= 6.0" }
    random = { source = "hashicorp/random", version = ">= 3.5" }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

resource "google_project_service" "apis" {
  for_each = toset([
    "spanner.googleapis.com", "sqladmin.googleapis.com", "run.googleapis.com", "artifactregistry.googleapis.com",
    "datastream.googleapis.com", "dataflow.googleapis.com", "secretmanager.googleapis.com",
  ])
  service            = each.key
  disable_on_destroy = false
}

# ---------------- Spanner (PostgreSQL interface) ----------------
resource "google_spanner_instance" "sc" {
  name             = "shadowcut"
  config           = "regional-${var.region}"
  display_name     = "shadowcut"
  processing_units = var.spanner_processing_units
  force_destroy    = true
  depends_on       = [google_project_service.apis]
}

resource "google_spanner_database" "wiki" {
  instance            = google_spanner_instance.sc.name
  name                = "wiki"
  database_dialect    = "POSTGRESQL"
  deletion_protection = false
  # The schema is applied with `shadowcut schema apply` through PGAdapter, exactly as locally.
}

# ---------------- Source: Cloud SQL for PostgreSQL 16 ----------------
resource "random_password" "pg" {
  length  = 24
  special = false
}

resource "google_sql_database_instance" "src" {
  name                = "shadowcut-src"
  database_version    = "POSTGRES_16"
  region              = var.region
  deletion_protection = false
  settings {
    tier              = var.cloudsql_tier
    availability_type = "ZONAL"
    disk_size         = 20
    database_flags {
      name  = "cloudsql.logical_decoding" # required by Debezium and by Datastream
      value = "on"
    }
    ip_configuration {
      ipv4_enabled = true # reached through the Cloud SQL connector; no authorized networks
    }
  }
  depends_on = [google_project_service.apis]
}

resource "google_sql_database" "wiki" {
  name     = "wiki"
  instance = google_sql_database_instance.src.name
}

resource "google_sql_user" "app" {
  name     = "shadowcut"
  instance = google_sql_database_instance.src.name
  password = random_password.pg.result
}

resource "google_secret_manager_secret" "pg_password" {
  secret_id = "shadowcut-pg-password"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_version" "pg_password" {
  secret      = google_secret_manager_secret.pg_password.id
  secret_data = random_password.pg.result
}

# ---------------- Images and identity ----------------
resource "google_artifact_registry_repository" "images" {
  location      = var.region
  repository_id = "shadowcut"
  format        = "DOCKER"
  depends_on    = [google_project_service.apis]
}

locals {
  image   = "${var.region}-docker.pkg.dev/${var.project_id}/shadowcut/shadowcut:${var.image_tag}"
  spanner = "projects/${var.project_id}/instances/${google_spanner_instance.sc.name}/databases/${google_spanner_database.wiki.name}"
}

resource "google_service_account" "run" {
  account_id   = "shadowcut-run"
  display_name = "Shadowcut Cloud Run"
}

resource "google_project_iam_member" "run_roles" {
  for_each = toset(["roles/spanner.databaseUser", "roles/cloudsql.client", "roles/secretmanager.secretAccessor"])
  project  = var.project_id
  role     = each.key
  member   = "serviceAccount:${google_service_account.run.email}"
}
