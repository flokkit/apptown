# Apply as an administrator. AppTown never runs or reads this configuration.
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}
variable "installation_id" { type = string }
variable "vpc_id" { type = string }
variable "cluster_name" { type = string }
variable "task_subnet_ids" { type = list(string) }
variable "listener_arn" {
  type    = string
  default = ""
}
variable "create_load_balancer" {
  type    = bool
  default = false
}
variable "public_subnet_ids" {
  type    = list(string)
  default = []
}
variable "certificate_arn" {
  type    = string
  default = ""
}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}
locals {
  installation_prefix = substr(sha256(var.installation_id), 0, 8)
  listener_arn        = var.create_load_balancer ? aws_lb_listener.https[0].arn : var.listener_arn
  rule_prefix         = "${replace(local.listener_arn, "listener/", "listener-rule/")}/*"
}
resource "aws_iam_user" "apptown" {
  name = "apptown-${local.installation_prefix}"
}
resource "aws_iam_role" "execution" {
  name               = "apptown-${local.installation_prefix}-task-execution"
  assume_role_policy = jsonencode({ "Version" : "2012-10-17", "Statement" : [{ "Effect" : "Allow", "Principal" : { "Service" : "ecs-tasks.amazonaws.com" }, "Action" : "sts:AssumeRole" }] })
}
resource "aws_iam_user_policy" "apptown" {
  name   = "apptown"
  user   = aws_iam_user.apptown.name
  policy = <<-POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Read",
      "Effect": "Allow",
      "Action": [
        "sts:GetCallerIdentity",
        "ec2:DescribeRegions",
        "ec2:DescribeSubnets",
        "ec2:DescribeRouteTables",
        "ec2:DescribeSecurityGroups",
        "elasticloadbalancing:Describe*",
        "acm:DescribeCertificate",
        "ecs:DescribeClusters",
        "ecs:DescribeServices",
        "ecs:DescribeTasks",
        "ecs:DescribeTaskDefinition",
        "ecs:ListTasks",
        "rds:DescribeDBInstances",
        "rds:DescribeDBSnapshots",
        "iam:GetRole",
        "tag:GetResources",
        "ecr:GetAuthorizationToken"
      ],
      "Resource": "*"
    },
    {
      "Sid": "CheckExecutionRole",
      "Effect": "Allow",
      "Action": [
        "iam:SimulatePrincipalPolicy"
      ],
      "Resource": "${aws_iam_role.execution.arn}"
    },
    {
      "Sid": "PassExecutionRole",
      "Effect": "Allow",
      "Action": [
        "iam:PassRole"
      ],
      "Resource": "${aws_iam_role.execution.arn}",
      "Condition": {
        "StringEquals": {
          "iam:PassedToService": "ecs-tasks.amazonaws.com"
        }
      }
    },
    {
      "Sid": "TaskDefinitionsRegister",
      "Effect": "Allow",
      "Action": [
        "ecs:RegisterTaskDefinition"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "TaskDefinitionsDeregister",
      "Effect": "Allow",
      "Action": [
        "ecs:DeregisterTaskDefinition"
      ],
      "Resource": "*"
    },
    {
      "Sid": "TaskDefinitionsDelete",
      "Effect": "Allow",
      "Action": [
        "ecs:DeleteTaskDefinitions"
      ],
      "Resource": "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task-definition/at-${local.installation_prefix}-*:*"
    },
    {
      "Sid": "EcsTagOnCreate",
      "Effect": "Allow",
      "Action": [
        "ecs:TagResource"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}",
          "ecs:CreateAction": [
            "RegisterTaskDefinition",
            "CreateService",
            "RunTask"
          ]
        }
      }
    },
    {
      "Sid": "ServicesCreate",
      "Effect": "Allow",
      "Action": [
        "ecs:CreateService"
      ],
      "Resource": "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:service/${var.cluster_name}/at-${local.installation_prefix}-*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "ServicesOwn",
      "Effect": "Allow",
      "Action": [
        "ecs:UpdateService",
        "ecs:DeleteService"
      ],
      "Resource": "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:service/${var.cluster_name}/at-${local.installation_prefix}-*",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "TasksRun",
      "Effect": "Allow",
      "Action": [
        "ecs:RunTask"
      ],
      "Resource": "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task-definition/at-${local.installation_prefix}-*:*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}",
          "ecs:cluster": "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:cluster/${var.cluster_name}"
        }
      }
    },
    {
      "Sid": "TasksStopOwn",
      "Effect": "Allow",
      "Action": [
        "ecs:StopTask"
      ],
      "Resource": "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task/${var.cluster_name}/*",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "SecurityGroupsCreate",
      "Effect": "Allow",
      "Action": [
        "ec2:CreateSecurityGroup"
      ],
      "Resource": "arn:aws:ec2:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:security-group/*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "SecurityGroupsInVpc",
      "Effect": "Allow",
      "Action": [
        "ec2:CreateSecurityGroup"
      ],
      "Resource": "arn:aws:ec2:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:vpc/${var.vpc_id}"
    },
    {
      "Sid": "SecurityGroupsTagOnCreate",
      "Effect": "Allow",
      "Action": [
        "ec2:CreateTags"
      ],
      "Resource": "arn:aws:ec2:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:security-group/*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}",
          "ec2:CreateAction": "CreateSecurityGroup"
        }
      }
    },
    {
      "Sid": "SecurityGroupsOwn",
      "Effect": "Allow",
      "Action": [
        "ec2:AuthorizeSecurityGroupIngress",
        "ec2:RevokeSecurityGroupIngress",
        "ec2:DeleteSecurityGroup"
      ],
      "Resource": "arn:aws:ec2:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:security-group/*",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "TargetGroupsCreate",
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:CreateTargetGroup"
      ],
      "Resource": "arn:aws:elasticloadbalancing:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:targetgroup/at-${local.installation_prefix}-*/*",
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "TargetGroupsOwn",
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:DeleteTargetGroup"
      ],
      "Resource": "arn:aws:elasticloadbalancing:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:targetgroup/at-${local.installation_prefix}-*/*"
    },
    {
      "Sid": "ListenerRulesCreate",
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:CreateRule"
      ],
      "Resource": [
        "${local.listener_arn}",
        "${local.rule_prefix}"
      ],
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "LoadBalancerTagOnCreate",
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:AddTags"
      ],
      "Resource": [
        "${local.rule_prefix}",
        "arn:aws:elasticloadbalancing:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:targetgroup/at-${local.installation_prefix}-*/*"
      ],
      "Condition": {
        "StringEquals": {
          "aws:RequestTag/apptown:instance": "${var.installation_id}",
          "elasticloadbalancing:CreateAction": [
            "CreateTargetGroup",
            "CreateRule"
          ]
        }
      }
    },
    {
      "Sid": "ListenerRulesOwn",
      "Effect": "Allow",
      "Action": [
        "elasticloadbalancing:DeleteRule"
      ],
      "Resource": "${local.rule_prefix}",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/apptown:instance": "${var.installation_id}"
        }
      }
    },
    {
      "Sid": "Images",
      "Effect": "Allow",
      "Action": [
        "ecr:CreateRepository",
        "ecr:DeleteRepository",
        "ecr:BatchDeleteImage",
        "ecr:TagResource",
        "ecr:BatchCheckLayerAvailability",
        "ecr:InitiateLayerUpload",
        "ecr:UploadLayerPart",
        "ecr:CompleteLayerUpload",
        "ecr:PutImage",
        "ecr:BatchGetImage",
        "ecr:GetDownloadUrlForLayer",
        "ecr:ListTagsForResource"
      ],
      "Resource": "arn:aws:ecr:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:repository/apptown/${local.installation_prefix}/*"
    },
    {
      "Sid": "Logs",
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup",
        "logs:PutRetentionPolicy",
        "logs:DeleteLogGroup",
        "logs:TagResource",
        "logs:ListTagsForResource"
      ],
      "Resource": "arn:aws:logs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:log-group:/apptown/${local.installation_prefix}/*"
    },
    {
      "Sid": "Parameters",
      "Effect": "Allow",
      "Action": [
        "ssm:PutParameter",
        "ssm:GetParameters",
        "ssm:ListTagsForResource",
        "ssm:DeleteParameters",
        "ssm:GetParametersByPath",
        "ssm:AddTagsToResource"
      ],
      "Resource": "arn:aws:ssm:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:parameter/apptown/${local.installation_prefix}/*"
    },
    {
      "Sid": "Databases",
      "Effect": "Allow",
      "Action": [
        "rds:CreateDBInstance",
        "rds:CreateDBSubnetGroup",
        "rds:AddTagsToResource",
        "rds:DeleteDBInstance",
        "rds:DeleteDBSubnetGroup",
        "rds:ListTagsForResource"
      ],
      "Resource": [
        "arn:aws:rds:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:db:at-${local.installation_prefix}-*",
        "arn:aws:rds:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:subgrp:at-${local.installation_prefix}-*",
        "arn:aws:rds:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:pg:default.*",
        "arn:aws:rds:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:og:default:*"
      ]
    }
  ]
}
POLICY
}
resource "aws_iam_role_policy" "execution" {
  name   = "execution"
  role   = aws_iam_role.execution.id
  policy = <<-POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ecr:GetAuthorizationToken"
      ],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecr:BatchCheckLayerAvailability",
        "ecr:BatchGetImage",
        "ecr:GetDownloadUrlForLayer"
      ],
      "Resource": "arn:aws:ecr:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:repository/apptown/${local.installation_prefix}/*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ],
      "Resource": "arn:aws:logs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:log-group:/apptown/${local.installation_prefix}/*:*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ssm:GetParameters"
      ],
      "Resource": "arn:aws:ssm:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:parameter/apptown/${local.installation_prefix}/*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "kms:Decrypt"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "kms:ViaService": "ssm.${data.aws_region.current.region}.amazonaws.com"
        },
        "ForAnyValue:StringEquals": {
          "kms:ResourceAliases": "alias/aws/ssm"
        }
      }
    }
  ]
}
POLICY
}
resource "aws_security_group" "load_balancer" {
  count       = var.create_load_balancer ? 1 : 0
  description = "HTTPS entry for AppTown applications"
  vpc_id      = var.vpc_id
  dynamic "ingress" {
    for_each = [80, 443]
    content {
      protocol    = "tcp"
      from_port   = ingress.value
      to_port     = ingress.value
      cidr_blocks = ["0.0.0.0/0"]
    }
  }
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}
resource "aws_lb" "apptown" {
  count              = var.create_load_balancer ? 1 : 0
  internal           = false
  load_balancer_type = "application"
  subnets            = var.public_subnet_ids
  security_groups    = [aws_security_group.load_balancer[0].id]
}
resource "aws_lb_listener" "http" {
  count             = var.create_load_balancer ? 1 : 0
  load_balancer_arn = aws_lb.apptown[0].arn
  port              = 80
  protocol          = "HTTP"
  default_action {
    type = "redirect"
    redirect {
      protocol    = "HTTPS"
      port        = "443"
      status_code = "HTTP_301"
    }
  }
}
resource "aws_lb_listener" "https" {
  count             = var.create_load_balancer ? 1 : 0
  load_balancer_arn = aws_lb.apptown[0].arn
  port              = 443
  protocol          = "HTTPS"
  certificate_arn   = var.certificate_arn
  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      message_body = "No application at this address"
      status_code  = "404"
    }
  }
}
output "region" { value = data.aws_region.current.region }
output "account" { value = data.aws_caller_identity.current.account_id }
output "execution_role_arn" { value = aws_iam_role.execution.arn }
output "listener_arn" { value = local.listener_arn }
output "user_name" { value = aws_iam_user.apptown.name }
output "cluster" { value = var.cluster_name }
output "task_subnets" { value = var.task_subnet_ids }
