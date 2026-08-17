# Terraform through the infrastructure operator

This opt-in example demonstrates Terraform execution through an externally
installed Infrakube controller in the infrastructure operator's KRO runtime.
It is not a native Faros Terraform backend and it is deliberately excluded from
`install/templates`, so ordinary provider bootstrap neither installs Infrakube
nor advertises a Terraform offering.

With `make tilt-cluster` running, use the three manual actions in order:

```sh
make terraform-install
make terraform-enable
make terraform-smoke
```

`terraform-install` builds controller and task images from a pinned Infrakube
commit, loads them into the `kcp-tilt` kind cluster, installs the CRDs and
controller, and waits for readiness. `terraform-enable` applies the checked-in
Template to the provider workspace and proves that the Template, APIExport,
tenant-facing cache, and runtime KRO graph converge. `terraform-smoke` creates
an isolated tenant and APIBinding, applies a cloud-free `terraform_data`
resource, checks the KRO-labeled Infrakube child and Kubernetes backend state,
then deletes the Faros parent and proves Terraform destroy completed.

The Kubernetes backend intentionally retains an empty state Secret and Lease
after destroy. The smoke test deletes only its exact test-owned artifacts after
proving that behavior. A production operator must choose and enforce its own
state-retention policy.

The source repository defaults to the pinned Faros development fork used by
the proof of concept. Override `INFRAKUBE_REPOSITORY` only when the requested
`INFRAKUBE_COMMIT` exists in that repository; the installer verifies the exact
checked-out commit before building either image.
