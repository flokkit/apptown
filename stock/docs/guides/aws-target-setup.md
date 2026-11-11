# AWS ECS prerequisites

These [CloudFormation](https://github.com/flokkit/apptown/blob/main/stock/docs/guides/aws-target-setup.yaml) and
[Terraform](https://github.com/flokkit/apptown/blob/main/stock/docs/guides/aws-target-setup.tf) samples prepare an existing AWS estate for
AppTown. An AWS administrator applies one of them. AppTown never runs or reads
the stack, Terraform state or outputs. The samples create an IAM user and task
execution role, without an access key. They do not create a VPC, subnet, cluster
or DNS record, or change an existing load balancer.

Supply the installation id, existing VPC id, task subnets, cluster name and
HTTPS listener ARN. The listener must belong to an internet-facing application
load balancer in that VPC, with an issued certificate for `*.<target domain>`.
The cluster must support FARGATE. Task subnets must all have internet-gateway
routes, or all have NAT-gateway routes. RDS database subnets must span at least
two zones in the same VPC; they default to the task subnets at attach.

CloudFormation also takes the installation prefix. Compute it from the exact
installation id, without a trailing newline:

```sh
printf %s '<installation id>' | shasum -a 256 | cut -c1-8
```

Terraform derives the same eight hexadecimal characters from `installation_id`.
Use the AWS provider's own configuration to select the administrator account and
region when applying Terraform. This is separate from AppTown's credentials.

Both samples default to using the listener supplied as an input. If the estate
has no load balancer, an administrator can explicitly enable
`CreateLoadBalancer` (Terraform: `create_load_balancer`) and supply public
subnets and an existing ACM certificate. This creates a new load balancer, its
security group and HTTP/HTTPS listeners. HTTP redirects to HTTPS; unmatched
HTTPS requests receive 404. This is a standing, billable resource managed by
the administrator, outside AppTown's deployment lifecycle.

Copy the outputs into the attach form: region, account, execution-role ARN,
listener ARN, cluster and task subnets. Create the user's access key separately
and put its pair in AppTown's instance secrets. Attach names those secrets as
`keyIdFrom` and `secretKeyFrom`; defaults are `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`. AppTown resolves the pair on each use. It does not use
environment credentials, profiles, an instance role or an assumed role.

An administrator points `*.<target domain>` at the load balancer's DNS name.
AppTown makes only its own rules on the supplied listener. ECS and RDS
service-linked roles must already exist. If missing, an administrator creates
them with:

```sh
aws iam create-service-linked-role --aws-service-name ecs.amazonaws.com
aws iam create-service-linked-role --aws-service-name rds.amazonaws.com
```

## Policy boundary and qualification

The JSON policies in both templates grant the same authority. Id-addressed
resources require this installation's existing tag for mutation. EC2, ECS and
ELB tagging is allowed only during the named create actions, so the key cannot
claim a foreign resource by retagging it. Security-group creation has separate
grants for the tagged new group and the existing VPC. Service and task ARNs
name the cluster; listener-rule ARNs derive from the supplied listener.
Name-addressed ECR, logs, SSM, RDS and target-group grants are restricted by the
installation prefix; the driver must still write and check their tags.

The key can read region-wide resources. Task-definition deregistration requires
wildcard authority: the key could deregister another team's revision, although
AppTown checks its owned records and live tags before doing so. AppTown cannot
establish that no additional policy grants the user broader access. Do not use
the reserved installation name ranges for unrelated resources.

Before enabling deployments, evaluate the rendered user policy with IAM's
`SimulateCustomPolicy`, supplying the actual resource ARNs and request context.
These are qualification cases, not a claim of completed AWS verification:

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

Then check attach's EC2 dry run under the actual key and exercise a disposable
release, rollback, copy, restore and take-down under that policy. A fake does
not prove IAM semantics, routing, certificate reachability or AWS adapter
correctness. The samples have not been applied or qualified against AWS.

Policy references: [EC2 security-group permissions](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-policy-examples.html#vpc-policy-examples-security-groups),
[ECS authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html),
[ELB authorization](https://docs.aws.amazon.com/service-authorization/latest/reference/list_elbv2.html)
and [ELB create-time tagging](https://docs.aws.amazon.com/elasticloadbalancing/latest/userguide/tagging-resources-during-creation.html).


## Attach and run apps

Enable the AWS ECS beta feature in instance settings. Choose **AWS ECS on
Fargate** when adding a deployment target, then copy the explicit attach values
from the template outputs. Put the key pair into instance secrets first. The
check records nothing; adding the target records its connector and target
together. The target page shows the load balancer address and a current DNS check.
Point the wildcard apps record at that load balancer yourself.

This target runs one public Linux/AMD64 part with an optional dedicated
PostgreSQL 16 or 17 instance. AppTown refuses multiple parts, shared databases,
object storage, data directories, gated placements and signed-in audiences before
any effect. Runtime and init secrets use separate immutable SSM paths for each
release generation; rollback keeps the previous generation's values.

Attach a backup store to enable portable Postgres copies. Jobs run in Fargate,
so there is no copy-runner host. Jobs list every non-template database and count
its user tables. Databases with tables get dumps; empty databases get a repeated
empty check. Only the provider-owned `rdsadmin` database is excluded. A failed
listing, count, dump, upload or restoration check keeps the instance. The set's
manifest and every item are read back from the backup store for size and digest.
The default Fargate scratch space is 20 GB; an export that fills it fails.

A final take-down stops writers and removes routing, then copies and checks the
data before deleting RDS. An explicit take-down without a final copy retains the
RDS instance, subnet group and database security group. They continue to cost
money. Deleting the instance deletes its automatic snapshots too; those snapshots
are separate from AppTown's backup sets.
