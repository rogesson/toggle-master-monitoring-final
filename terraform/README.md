# Terraform

The default `academy_mode = true` reads the existing `LabRole` and does not create IAM roles or policies. For a personal AWS account set it to `false`; Terraform then creates the EKS and node roles.

The state backend is S3 with encryption and native lockfile support. Create a private, versioned S3 bucket in your AWS account first, then pass its name and region through an untracked `backend.hcl` file. Never commit credentials, `terraform.tfvars`, `backend.hcl`, or secret values.

## AWS Academy EKS workers

The Academy SCP denies `ec2:DescribeInstances` to `LabRole`. On the current AL2023 worker AMI, `nodeadm` calls that API while selecting the Kubernetes node name, so EC2 instances can be `InService` while never registering as Kubernetes Nodes. The worker launch template enables the EKS `InstanceIdNodeName` feature gate, which removes that node-name dependency. This requires EKS access entries and an `EC2` entry for the worker role so the instance-ID node name is authorized; the existing `aws-auth` ConfigMap remains enabled for other mappings.

Applying this migration changes EKS authentication from `CONFIG_MAP` to `API_AND_CONFIG_MAP`. AWS does not allow changing back to a mode that removes access entries. Terraform also replaces the existing managed node group because EKS cannot add a custom launch template to an existing managed node group. Review both changes in the Terraform plan before applying. After new nodes register, check the `aws-node` CNI logs for any separate SCP-denied EC2 API calls.

```bash
terraform -chdir=terraform init -backend-config=backend.hcl
terraform -chdir=terraform plan -var-file=terraform.tfvars
terraform -chdir=terraform apply -var-file=terraform.tfvars
```

The EKS and PostgreSQL versions are intentionally not hardcoded. AWS Academy retires versions by region; omitting them makes AWS select a currently supported default in the configured region.
