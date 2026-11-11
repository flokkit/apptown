# AWS ECS target setup

An AWS ECS on Fargate target runs each deployment as one public part on
Fargate, in an AWS estate you already have, with an optional dedicated
PostgreSQL RDS instance. This target is in beta. The samples below have not
been applied or qualified against AWS.

## What you need

- The **AWS ECS targets** feature turned on under **Setup → Features**
  ([Features](features.md)).
- An existing VPC, and in it:
  - an ECS cluster that supports FARGATE;
  - task subnets that all route through an internet gateway, or all through a
    NAT gateway;
  - an HTTPS listener on an internet-facing application load balancer in that
    VPC, with an issued certificate for `*.<apps domain>`;
  - optionally, database subnets in at least two zones (they default to the
    task subnets).
- The ECS and RDS service-linked roles. If they are missing, an administrator
  creates them:

  ```sh
  aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com
  aws iam create-service-linked-role --aws-service-name rds.amazonaws.com
  ```

- A container registry attached to each project that deploys here. Images
  must be published by digest for `linux/amd64`.
- For database copies, a backup store ([Deployment
  backups](deployment-backups.md)). Copies run as one-off Fargate tasks, so no
  copy runner is needed.

## The credential

These [CloudFormation](https://github.com/flokkit/apptown/blob/main/stock/docs/guides/aws-target-setup.yaml) and
[Terraform](https://github.com/flokkit/apptown/blob/main/stock/docs/guides/aws-target-setup.tf) samples make an IAM user and a task
execution role for AppTown, without an access key. An AWS administrator applies
one of them. AppTown never runs or reads the stack, Terraform state or
outputs. The samples do not create a VPC, subnet, cluster or DNS record, or
change an existing load balancer.

Give them the installation id, the VPC id, the task subnets, the cluster name
and the HTTPS listener ARN. CloudFormation also takes the installation prefix.
Compute it from the exact installation id, without a trailing newline:

```sh
printf %s '<installation id>' | shasum -a 256 | cut -c1-8
```

Terraform derives the same eight hexadecimal characters from `installation_id`.
Select the administrator account and region with the AWS provider's own
configuration when applying Terraform. That is separate from AppTown's
credentials.

Both samples use the listener you supply. If the estate has no load balancer,
an administrator can turn on `CreateLoadBalancer` (Terraform:
`create_load_balancer`) and supply public subnets and an existing ACM
certificate. This creates a load balancer, its security group and HTTP and
HTTPS listeners. HTTP redirects to HTTPS; unmatched HTTPS requests get 404. It
is a standing, billable resource the administrator manages, outside
AppTown's deployments.

Create the user's access key separately, and store the pair as AppTown
secrets, under **Integrations › Secrets** or with `apptown secret set`. The
defaults are `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. AppTown reads the
pair each time it uses it. It does not use environment credentials, profiles,
an instance role or an assumed role.

### What the key may do

The JSON policies in both samples grant the same rights. Changing a resource
named by id requires this installation's existing tag. EC2, ECS and ELB
tagging is allowed only during the named create actions, so the key cannot
claim a foreign resource by tagging it. Creating a security group has separate
grants for the tagged new group and the existing VPC. Service and task ARNs
name the cluster; listener-rule ARNs derive from the supplied listener. ECR,
logs, SSM, RDS and target-group grants, which name resources, are limited to
the installation prefix; AppTown still writes and checks their tags.

The key can read region-wide resources. Deregistering a task definition needs
a wildcard grant: the key could deregister another team's revision, although
AppTown checks its own records and the live tags before it does. AppTown cannot
tell whether another policy gives the user more. Do not use the installation's
reserved name ranges for anything else.

Before turning on deployments, evaluate the user policy with IAM's
`SimulateCustomPolicy`, giving the actual resource ARNs and request context:

| Case | Expected |
| --- | --- |
| Own tagged service, task definition, task, target group and listener rule creates | allowed |
| Security-group create: own request tag, named VPC | allowed |
| Security-group create: wrong request tag, or another VPC | denied |
| Own service update, own task stop, own listener-rule delete | allowed |
| Rule create on another listener | denied |
| Standalone retag of a foreign security group, service or listener rule | denied |
| Foreign task stop in the shared cluster | denied |
| Foreign listener-rule delete | denied |

Then run a disposable release, rollback, copy, restore and take-down under that
policy. A fake does not prove IAM rules, routing, certificate reachability or
AppTown's AWS code.

Policy references: [EC2 security-group permissions](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-policy-examples.html#vpc-policy-examples-security-groups),
[ECS authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html),
[ELB authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_elbv2.html)
and [ELB create-time tagging](https://docs.aws.amazon.com/elasticloadbalancing/latest/userguide/tagging-resources-during-creation.html).

## Add the target

Open **Deployments › Targets → Add a deployment target** and choose **AWS ECS
on Fargate**. Copy the values from the sample's outputs.

| Field | What to enter |
| --- | --- |
| name | A short name for the target, such as `aws-syd` |
| access key secret | The name of the secret holding the access key id |
| secret key secret | The name of the secret holding the secret access key |
| expected account id | The 12-digit account the key must act in |
| region | The region, such as `ap-southeast-2` |
| cluster | The cluster's name, not its ARN |
| task subnets (comma separated) | The task subnet ids |
| database subnets (optional; uses task subnets) | Subnet ids for RDS, in at least two zones |
| HTTPS listener ARN | The listener's ARN |
| execution role ARN | The task execution role's ARN |
| apps domain | The domain apps are reached under, such as `apps.example.com` |

**Check** confirms that the key acts in the expected account, the region is
on, the cluster is active with FARGATE, the subnets and listener are in one
VPC with the routes and certificate above, the execution role is trusted by
ECS tasks, the service-linked roles exist, and the key may create a security
group (an EC2 dry run). It answers with the load balancer's address. Nothing
is recorded until you press **Add target**.

Point a wildcard DNS record, `*.<apps domain>`, at the load balancer yourself.
AppTown adds only its own rules to the listener.

## Check it worked

The target's page shows **Target check:** with the load balancer, and a line
saying whether the wildcard apps address resolves to that load balancer, and
when it was read.

## What AppTown creates and deletes

For each deployment AppTown creates an ECS service, task definitions, a target
group, a listener rule, an ECR repository, SSM parameters, a log group and a
security group, and for Postgres an RDS instance with its subnet group and
security group. Runtime and init secrets are separate SSM paths for each
release, so a rollback keeps the earlier values.

A final take-down stops the app and removes its routing, then copies and checks
the database before it deletes the RDS instance. A take-down without a final
copy keeps the RDS instance, its subnet group and its security group, listed as
retained resources. Deleting the instance also deletes its automatic
snapshots; those are separate from AppTown's backups.

AppTown never changes the VPC, subnets, cluster, load balancer, its other
listener rules, DNS or the sample's stack.

## Costs and limits

- AWS bills for Fargate tasks, RDS, the load balancer and the rest. The
  **Capacity** page lists the size each part runs on; AWS prices are not read,
  so the cost reads unknown. Retained RDS resources keep costing money.
- The app must have one public part. Multiple parts, shared databases, object
  storage, data folders, restored copies and audiences other than public are
  refused before anything is made at AWS.
- Postgres must be version 16 or 17.
- **Start**, **Stop** and **Restart** are not offered.
- Database copies list every database except RDS's own `rdsadmin`. A database
  with tables is dumped; an empty one is checked again as empty. Each dump is
  restored and checked in a scratch database. A failed listing, count, dump,
  upload or check keeps the instance. Fargate's default scratch space is 20 GB;
  a copy that fills it fails.
- RDS's automatic snapshots are shown separately and are never AppTown backups.
