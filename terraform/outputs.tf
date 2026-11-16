output "spanner_database" {
  value = local.spanner
}

output "cloudsql_connection_name" {
  value = google_sql_database_instance.src.connection_name
}

output "demoapp_url" {
  value = google_cloud_run_v2_service.demoapp.uri
}
