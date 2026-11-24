---
title: "Utilizing On-Demand Capacity Reservations and Capacity Blocks"
linkTitle: "Utilizing ODCRs and Capacity Blocks"
---

<i class="fa-solid fa-circle-info"></i> <b>Feature State: </b> [Beta]({{<ref "../reference/settings#feature-gates" >}})

Karpenter introduced native support for [EC2 On-Demand Capacity Reservations](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-capacity-reservations.html)  (ODCRs) in [v1.3](https://github.com/aws/karpenter-provider-aws/releases/tag/v1.3.0), enabling users to select upon and prioritize specific capacity reservations.
In [v1.6](https://github.com/aws/karpenter-provider-aws/releases/tag/v1.6.0), this support was expanded to include [EC2 Capacity Blocks for ML](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-capacity-blocks.html). In [v1.10](https://github.com/aws/karpenter-provider-aws/releases/tag/v1.10.0), this was further extended to support [Interruptible Capacity Reservations](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/interruptible-capacity-reservations.html).
To enable native ODCR support, ensure the [`ReservedCapacity` feature gate]({{< relref "../reference/settings#feature-gates" >}}) is enabled.

{{% alert title="Note" color="primary" %}}
If you were previously utilizing `open` ODCRs using Karpenter, review the [migration section]({{< relref "#migrating-from-previous-versions" >}}) of this task before enabling this feature.
{{% /alert %}}

## Selecting Capacity Reservations

To configure native ODCR support, you will need to make updates to both your EC2NodeClass and NodePool.
First, you should configure `capacityReservationSelectorTerms` on your EC2NodeClass.
Similar to `amiSelectorTerms`, you can specify a number of terms which are ANDed together to select ODCRs in your AWS account.
The following example demonstrates how to select all capacity reservations tagged with `application: foobar` in addition to `cr-56fac701cc1951b03`:

```yaml
capacityReservationSelectorTerms:
- tags:
    application: foobar
- id: cr-56fac701cc1951b03
```

{{% alert title="Note" color="primary" %}}
Capacity blocks are modeled as on-demand capacity reservations in EC2.
To select capacity blocks, specify them in your `capacityReservationSelectorTerms` in the same way you would for a default ODCR.
{{% /alert %}}

For more information on configuring `capacityReservationSelectorTerms`, see the [NodeClass docs]({{< relref "../concepts/nodeclasses#speccapacityreservationselectorterms" >}}).

Additionally, you will need to update your NodePool to be compatible with ODCRs.
Karpenter doesn't model ODCRs as standard on-demand capacity, and instead uses a dedicated capacity type: `reserved`.
For a NodePool to utilize ODCRs, it must be compatible with `karpenter.sh/capacity-type: reserved`.
The following example demonstrates how to configure a NodePool to prioritize ODCRs and fallback to on-demand capacity:

```yaml
requirements:
- key: karpenter.sh/capacity-type
  operator: In
  values: ['reserved', 'on-demand']
```

Additionaly, Karpenter supports the following scheduling labels:

| Label                                                  | Example                       | Description                                       |
| ------------------------------------------------------ | ----------------------------- | ------------------------------------------------- |
| `karpenter.k8s.aws/capacity-reservation-id`            | `cr-56fac701cc1951b03`        | The capacity reservation's ID                     |
| `karpenter.k8s.aws/capacity-reservation-type`          | `default` or `capacity-block` | The type of capacity reservation                  |
| `karpenter.k8s.aws/capacity-reservation-interruptible` | `true` or `false`             | Whether the capacity reservation is interruptible |

These labels will only be present on reserved nodes.
They are supported as NodePool requirements and as pod scheduling constaints (e.g. [node affinity](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity)).

{{% alert title="Warning" color="warning" %}}
Karpenter does **not** support open matching for ODCRs.
This means that all ODCRs you wish to utilize, even those with `open` instance eligibility, must be included in your NodeClass' `spec.capacityReservationSelectorTerms`.
{{% /alert %}}

## Prioritization Behavior

NodePools are not limited to a single compatible capacity-type -- they can be compatible with any combination of the available capacity-types (`on-demand`, `spot`, and `reserved`).
Consider the following NodePool requirements:

```yaml
requirements:
- key: karpenter.sh/capacity-type
  operator: In
  values: ['reserved', 'on-demand', 'spot']
```

In this example, the NodePool is compatible with all capacity types.
Karpenter will prioritize ODCRs, but if none are available or none are compatible with the pending workloads it will fallback to spot or on-demand.
Similarly, Karpenter will prioritize reserved capacity during consolidation.
Since ODCRs are pre-paid, Karpenter will model them as free and consolidate spot / on-demand nodes when possible.

## Migrating On-Demand Nodes into Reservations

<i class="fa-solid fa-circle-info"></i> <b>Feature State: </b> [Alpha]({{<ref "../reference/settings#feature-gates" >}})

When an ODCR gains free capacity, for example because nodes in it were consolidated away or the reservation was created or extended, on-demand nodes that could run in it keep running as on-demand.
Consolidation can replace them with new nodes in the reservation, but that launches new instances and evicts their pods.
With the `ReservedCapacityMigration` [feature gate]({{<ref "../reference/settings#feature-gates" >}}) (Helm: `settings.featureGates.reservedCapacityMigration`), Karpenter instead moves the running on-demand instance into the reservation in place, using EC2's [support for modifying the Capacity Reservation settings of a running instance](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/capacity-reservations-modify-instance.html).
The instance isn't stopped or replaced, and its pods keep running.

Karpenter migrates an on-demand node into a reservation selected by its EC2NodeClass when all of the following are true:

* The reservation is an active, non-interruptible On-Demand Capacity Reservation (not a Capacity Block) for the node's instance type and Availability Zone, and has free capacity.
* The node's NodePool allows the `reserved` capacity type and the reservation, so the migrated node isn't drifted.
* Every pod on the node, including DaemonSet pods, still matches the node on its node selector and required node affinity once the node is relabeled as reserved, no DaemonSet that doesn't run on the node today would select it afterwards, and no pod uses `karpenter.sh/capacity-type` or a `karpenter.k8s.aws/capacity-reservation-*` label as the topology key of a required pod affinity, pod anti-affinity, or `DoNotSchedule` topology spread constraint involving the node.
* The node is initialized and isn't being disrupted, deleted, rebooted, nominated for pending pods, or annotated with `karpenter.sh/do-not-disrupt: "true"`.
* The instance was launched with a capacity reservation preference of `none`, which Karpenter sets on every on-demand instance it launches while the `ReservedCapacity` feature gate is enabled, or it targets a reservation that is no longer active, like a node Karpenter launched into an ODCR that since expired or was cancelled. EC2 can't move running instances launched with the default `open` preference.

To migrate a node, Karpenter taints it with `karpenter.sh/reservation-migration:NoSchedule` so no new pods schedule against its on-demand labels, waits for in-flight pod bindings to settle, and checks the node's pods again.
It then records the target reservation in the NodeClaim's `karpenter.sh/reservation-migration-target` annotation, moves the instance into the reservation, relabels the NodeClaim and Node with `karpenter.sh/capacity-type: reserved` and the reservation's `karpenter.k8s.aws/capacity-reservation-*` labels, and removes the taint.
Karpenter migrates at most 10 nodes at a time, and never more nodes into a reservation than it has free capacity.
Karpenter checks the node's pods one last time right before it asks EC2 to move the instance. Pods that tolerate the `karpenter.sh/reservation-migration` taint, or are bound with `spec.nodeName`, can still land on the node in the seconds between that request and the relabel, so Karpenter relabels the node regardless and emits a `ReservationMigrationMismatched` warning event if they don't match its new labels.
If EC2 doesn't accept the request within 5 minutes, or accepts it but the instance isn't reported in the reservation within 30 minutes, Karpenter abandons the migration and removes the taint.

Karpenter treats an instance that explicitly targets the reservation it runs in as `reserved`, including instances moved into a reservation outside of Karpenter. If you disable the feature gate, in-flight migrations still complete. If you downgrade to a version of Karpenter without this feature while a migration is in flight, remove the `karpenter.sh/reservation-migration` taint and the `karpenter.sh/reservation-migration-target` annotation yourself.

{{% alert title="Note" color="primary" %}}
Migrating nodes requires the [ec2:ModifyInstanceCapacityReservationAttributes](https://docs.aws.amazon.com/service-authorization/latest/reference/list_amazonec2.html#amazonec2-ModifyInstanceCapacityReservationAttributes) action on the instances Karpenter manages and on the reservations it targets. See the `AllowScopedCapacityReservationMigration` and `AllowCapacityReservationMigrationTargets` statements of the [controller policy]({{<ref "../reference/cloudformation#allowscopedcapacityreservationmigration" >}}).
{{% /alert %}}

## Disrupting Nodes in Full Reservations

For [Drift]({{<ref "../concepts/disruption#drift" >}}) and [Node Auto Repair]({{<ref "../concepts/disruption#node-auto-repair" >}}), Karpenter pre-spins a replacement before it terminates a node.
When a node's reservation is full and its pods can't run anywhere else, such as on on-demand or spot capacity in the same NodePool or in a lower-weight NodePool, Karpenter can't pre-spin a replacement, and Drift and Node Auto Repair are blocked for that node.
To replace these nodes within the same reservation, enable the `TerminateFirstDrift` and `TerminateFirstRepair` [feature gates]({{<ref "../reference/settings#feature-gates" >}}). `TerminateFirstRepair` has no effect unless `NodeRepair` is enabled, and has no effect with `--legacy-node-repair`.
Karpenter will then terminate the node first and launch its replacement into the freed reservation slot.
The gates apply to every NodePool in the cluster, including static NodePools, which also terminate nodes first when they reach their `limits.nodes`.
See [Terminate-First Disruption]({{<ref "../concepts/disruption#terminate-first-disruption" >}}) for details.

{{% alert title="Warning" color="warning" %}}
Terminate-first disruption can leave the node's pods pending for a long time. Read the Warning in [Terminate-First Disruption]({{<ref "../concepts/disruption#terminate-first-disruption" >}}) before you enable it.
{{% /alert %}}

## Expiration

An instance launched into an ODCR is not necessarily in that ODCR indefinitely.
The ODCR could expire, be cancelled, or the instance could be manually removed from the ODCR.
If any of these occur, and Karpenter detects that the instance no longer belongs to an ODCR, it will update the `karpenter.sh/capacity-type` label to `on-demand`.

### Capacity Blocks

Unlike default ODCRs, Capacity Blocks must have an end time.
Additionally, instances launched into a capacity block will be terminated by EC2 ahead of the end time, rather than becoming standard on-demand capacity.

From the [AWS docs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-capacity-blocks.html):

> You can use all the instances you reserved until 30 minutes (for instance types) or 60 minutes (for UltraServer type) before the end time of the Capacity Block.
> With 30 minutes (for instance types) or 60 minutes (for UltraServer types) left in your Capacity Block reservation, we begin terminating any instances that are running in the Capacity Block.
> We use this time to clean up your instances before delivering the Capacity Block to the next customer.

Karpenter will preemptively begin draining nodes launched for capacity blocks 10 minutes before EC2 begins termination, ensuring your workloads can gracefully terminate before reclaimation.

### Interruptible Capacity Reservations

Unlike default ODCRs, capacity launched from interruptible ODCRs can be interrupted when capacity is reclaimed back the source ODCR.

From the [AWS docs](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/interruptible-capacity-reservations.html), when capacity is reclaimed:

> Running instances receive a 2-minute interruption warning through EventBridge events.
> After the notice period, running instances in the reclaimed capacity enter a shutting down state and get terminated.

Karpenter will begin draining nodes launched for IODCRs when the 2-minute interruption warning is recieved.

## Migrating From Previous Versions

Although it was not natively supported, it was possible to utilize ODCRs on previous versions of Karpenter.
If a NodeClaim's requirements happened to be compatible with an open ODCR in the target AWS account, it may have launched an instance into that open ODCR.
This could be ensured by constraining a NodePool such that it was only compatible with the desired open ODCR, and limits could be used to enable fallback to a different NodePool once the ODCR was exhausted.
This behavior is no longer supported when native on-demand capacity support is enabled.

If you were relying on this behavior, you should configure your `EC2NodeClasses` to select the desired ODCRs **before** enabling the feature gate.
You should also ensure any NodePools which you wish to use with ODCRs are compatible with `karpenter.sh/capacity-type: reserved`.
Performing these steps before enabling the feature gate will ensure that Karpenter can immediately continue utilizing your reservations, rather than falling back to on-demand.
