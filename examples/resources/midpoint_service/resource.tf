# An application, with the built-in Application archetype.
resource "midpoint_service" "dev_k8s" {
  name         = "dev-k8s"
  display_name = "Dev Kubernetes cluster"
  description  = "Kubernetes cluster for development."
  archetype    = "00000000-0000-0000-0000-000000000329" # Application
}
