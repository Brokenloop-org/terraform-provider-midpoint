# An object mark shown on objects nobody should change by hand.
resource "midpoint_mark" "protected" {
  name        = "Protected"
  description = "Objects managed by Terraform."

  display = {
    label = "Protected"
    icon  = { cssClass = "fa fa-lock", color = "red" }
  }

  event = { enabledByDefault = false }

  # Anything else, in midPoint naming.
  extra = {
    documentation = "Set by the platform team."
  }
}
