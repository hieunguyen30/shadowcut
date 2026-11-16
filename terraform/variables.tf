variable "project_id" {
  type        = string
  description = "GCP project for the (pending) experiment resources"
}

variable "region" {
  type    = string
  default = "us-central1"
}

variable "spanner_processing_units" {
  type        = number
  default     = 1000 # 1 node: smallest size where hotspot behaviour (Exp 3) is meaningful
  description = "Spanner compute capacity"
}

variable "cloudsql_tier" {
  type    = string
  default = "db-custom-2-7680"
}

variable "image_tag" {
  type    = string
  default = "dev"
}
