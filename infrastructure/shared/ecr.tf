# ---------------------------------------------------------
# ECR — the stuff-expo-ota server image, built and pushed by
# the fork's Jenkins CI; deployed by Spinnaker to staging and
# prod. One repo shared by both environments (images are
# environment-agnostic), hence this separate "shared" state.
# ---------------------------------------------------------
resource "aws_ecr_repository" "stuff_expo_ota" {
  name                 = "stuff-expo-ota"
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "stuff_expo_ota" {
  repository = aws_ecr_repository.stuff_expo_ota.name
  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after 14 days"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = 14
        }
        action = { type = "expire" }
      }
    ]
  })
}

# Cross-account pull: the application clusters run in the nebula nonprod/prod
# accounts and pull this image from the shared-services registry.
resource "aws_ecr_repository_policy" "cross_account_pull" {
  repository = aws_ecr_repository.stuff_expo_ota.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "AllowCrossAccountPull"
        Effect = "Allow"
        Principal = {
          AWS = [
            "arn:aws:iam::781247136068:root", # nebula non-prod
            "arn:aws:iam::522778376395:root", # nebula prod
          ]
        }
        Action = [
          "ecr:GetDownloadUrlForLayer",
          "ecr:BatchGetImage",
          "ecr:BatchCheckLayerAvailability",
        ]
      }
    ]
  })
}
