/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package capacitytype_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	coretest "sigs.k8s.io/karpenter/pkg/test"

	"github.com/aws/karpenter-provider-aws/pkg/apis"
	v1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"
	"github.com/aws/karpenter-provider-aws/pkg/cloudprovider"
	"github.com/aws/karpenter-provider-aws/pkg/controllers/capacityreservation/capacitytype"
	"github.com/aws/karpenter-provider-aws/pkg/fake"
	"github.com/aws/karpenter-provider-aws/pkg/operator/options"
	"github.com/aws/karpenter-provider-aws/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"

	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

var ctx context.Context
var stop context.CancelFunc
var env *coretest.Environment
var awsEnv *test.Environment
var controller *capacitytype.Controller

func TestAWS(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "SSM Invalidation Controller")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...), coretest.WithFieldIndexers(coretest.NodeProviderIDFieldIndexer(ctx)))
	ctx = options.ToContext(ctx, test.Options())
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx, stop = context.WithCancel(ctx)
	awsEnv = test.NewEnvironment(ctx, env)

	cloudProvider := cloudprovider.New(awsEnv.InstanceTypesProvider, awsEnv.InstanceProvider, events.NewRecorder(&record.FakeRecorder{}),
		env.Client, awsEnv.AMIProvider, awsEnv.SecurityGroupProvider, awsEnv.CapacityReservationProvider, awsEnv.PlacementGroupProvider, awsEnv.InstanceTypeStore, lo.ToPtr(""))
	controller = capacitytype.NewController(env.Client, cloudProvider, awsEnv.InstanceProvider)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = Describe("Capacity Reservation Capacity Type Controller", func() {
	var nodeClaim *karpv1.NodeClaim
	var node *corev1.Node
	var reservationID string
	BeforeEach(func() {
		reservationID = "cr-foo"
		instance := ec2types.Instance{
			ImageId:               lo.ToPtr(fake.ImageID()),
			InstanceType:          ec2types.InstanceType("m5.large"),
			SubnetId:              lo.ToPtr(fake.SubnetID()),
			SpotInstanceRequestId: nil,
			State: &ec2types.InstanceState{
				Name: ec2types.InstanceStateNameRunning,
			},
			InstanceId:            lo.ToPtr(fake.InstanceID()),
			CapacityReservationId: &reservationID,
			CapacityReservationSpecification: &ec2types.CapacityReservationSpecificationResponse{
				CapacityReservationPreference: ec2types.CapacityReservationPreferenceCapacityReservationsOnly,
			},
			Placement: &ec2types.Placement{
				AvailabilityZone: lo.ToPtr("test-zone-1a"),
			},
			SecurityGroups: []ec2types.GroupIdentifier{{GroupId: lo.ToPtr(fake.SecurityGroupID())}},
		}
		awsEnv.EC2API.DescribeInstancesBehavior.Output.Set(&ec2.DescribeInstancesOutput{
			Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{instance}}},
		})

		nodeClaim = coretest.NodeClaim(karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					karpv1.CapacityTypeLabelKey:          karpv1.CapacityTypeReserved,
					corecloudprovider.ReservationIDLabel: reservationID,
					v1.LabelCapacityReservationType:      string(v1.CapacityReservationTypeDefault),
					karpv1.NodeRegisteredLabelKey:        "true",
				},
			},
			Status: karpv1.NodeClaimStatus{
				ProviderID: fmt.Sprintf("aws:///test-zone-1a/%s", *instance.InstanceId),
			},
		})
		node = coretest.NodeClaimLinkedNode(nodeClaim)
	})
	It("should demote nodeclaims and nodes from reserved to on-demand", func() {
		ExpectApplied(ctx, env.Client, nodeClaim, node)
		ExpectSingletonReconciled(ctx, controller)

		// Since the backing instance is still under a capacity reservation, we shouldn't demote the nodeclaim or node
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeReserved))
		Expect(nodeClaim.Labels).To(HaveKeyWithValue(corecloudprovider.ReservationIDLabel, reservationID))
		Expect(nodeClaim.Labels).To(HaveKeyWithValue(v1.LabelCapacityReservationType, string(v1.CapacityReservationTypeDefault)))
		node = ExpectExists(ctx, env.Client, node)
		Expect(node.Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeReserved))
		Expect(node.Labels).To(HaveKeyWithValue(corecloudprovider.ReservationIDLabel, reservationID))
		Expect(nodeClaim.Labels).To(HaveKeyWithValue(v1.LabelCapacityReservationType, string(v1.CapacityReservationTypeDefault)))

		out := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
		out.Reservations[0].Instances[0].CapacityReservationId = nil
		awsEnv.EC2API.DescribeInstancesBehavior.Output.Set(out)

		// Now that the backing instance is no longer part of a capacity reservation, we should demote the resources by
		// updating the capacity type to on-demand and removing the reservation ID label.
		ExpectSingletonReconciled(ctx, controller)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeOnDemand))
		Expect(nodeClaim.Labels).ToNot(HaveKey(corecloudprovider.ReservationIDLabel))
		Expect(nodeClaim.Labels).ToNot(HaveKey(v1.LabelCapacityReservationType))
		node = ExpectExists(ctx, env.Client, node)
		Expect(node.Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeOnDemand))
		Expect(node.Labels).ToNot(HaveKey(corecloudprovider.ReservationIDLabel))
		Expect(node.Labels).ToNot(HaveKey(v1.LabelCapacityReservationType))
	})
	It("should not demote a nodeclaim that moved into the reservation after the instances were listed", func() {
		listed := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
		listed.Reservations[0].Instances[0].CapacityReservationId = nil
		current := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
		current.Reservations[0].Instances[0].CapacityReservationSpecification = &ec2types.CapacityReservationSpecificationResponse{
			CapacityReservationTarget: &ec2types.CapacityReservationTargetResponse{CapacityReservationId: &reservationID},
		}
		awsEnv.EC2API.DescribeInstancesBehavior.Output.Reset()
		// MultiOut pops the last output first: the first describe serves the listing, the second the confirmation
		// before demoting
		awsEnv.EC2API.DescribeInstancesBehavior.MultiOut.Add(current)
		awsEnv.EC2API.DescribeInstancesBehavior.MultiOut.Add(listed)

		ExpectApplied(ctx, env.Client, nodeClaim, node)
		ExpectSingletonReconciled(ctx, controller)
		Expect(ExpectExists(ctx, env.Client, nodeClaim).Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeReserved))
		Expect(ExpectExists(ctx, env.Client, node).Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeReserved))
	})
	It("should not demote a nodeclaim that is migrating into a reservation", func() {
		out := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
		out.Reservations[0].Instances[0].CapacityReservationId = nil
		awsEnv.EC2API.DescribeInstancesBehavior.Output.Set(out)
		nodeClaim.Annotations = map[string]string{karpv1.ReservationMigrationTargetAnnotationKey: reservationID}

		ExpectApplied(ctx, env.Client, nodeClaim, node)
		ExpectSingletonReconciled(ctx, controller)
		Expect(ExpectExists(ctx, env.Client, nodeClaim).Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeReserved))
	})
	It("should remove the interruptible label when demoting", func() {
		nodeClaim.Labels[v1.LabelCapacityReservationInterruptible] = "false"
		node.Labels[v1.LabelCapacityReservationInterruptible] = "false"
		out := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
		out.Reservations[0].Instances[0].CapacityReservationId = nil
		awsEnv.EC2API.DescribeInstancesBehavior.Output.Set(out)

		ExpectApplied(ctx, env.Client, nodeClaim, node)
		ExpectSingletonReconciled(ctx, controller)
		Expect(ExpectExists(ctx, env.Client, nodeClaim).Labels).ToNot(HaveKey(v1.LabelCapacityReservationInterruptible))
		Expect(ExpectExists(ctx, env.Client, node).Labels).ToNot(HaveKey(v1.LabelCapacityReservationInterruptible))
	})
	Context("Promotion", func() {
		// The NodeClaim and Node are on-demand, while the instance runs in the reservation, e.g. after it waited for
		// capacity in a reservation it targeted.
		BeforeEach(func() {
			for _, labels := range []map[string]string{nodeClaim.Labels, node.Labels} {
				labels[karpv1.CapacityTypeLabelKey] = karpv1.CapacityTypeOnDemand
				delete(labels, corecloudprovider.ReservationIDLabel)
				delete(labels, v1.LabelCapacityReservationType)
			}
			out := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
			out.Reservations[0].Instances[0].CapacityReservationSpecification = &ec2types.CapacityReservationSpecificationResponse{
				CapacityReservationTarget: &ec2types.CapacityReservationTargetResponse{CapacityReservationId: &reservationID},
			}
			awsEnv.EC2API.DescribeInstancesBehavior.Output.Set(out)
		})
		It("should promote nodeclaims and nodes from on-demand to reserved once their instance joins a reservation", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, node)
			ExpectSingletonReconciled(ctx, controller)
			for _, labels := range []map[string]string{ExpectExists(ctx, env.Client, nodeClaim).Labels, ExpectExists(ctx, env.Client, node).Labels} {
				Expect(labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeReserved))
				Expect(labels).To(HaveKeyWithValue(corecloudprovider.ReservationIDLabel, reservationID))
				Expect(labels).To(HaveKeyWithValue(v1.LabelCapacityReservationType, string(v1.CapacityReservationTypeDefault)))
				Expect(labels).To(HaveKeyWithValue(v1.LabelCapacityReservationInterruptible, "false"))
			}
		})
		It("should not promote a nodeclaim that is migrating into a reservation", func() {
			nodeClaim.Annotations = map[string]string{karpv1.ReservationMigrationTargetAnnotationKey: reservationID}
			ExpectApplied(ctx, env.Client, nodeClaim, node)
			ExpectSingletonReconciled(ctx, controller)
			Expect(ExpectExists(ctx, env.Client, nodeClaim).Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeOnDemand))
		})
		It("should not promote a nodeclaim whose instance left the reservation after the instances were listed", func() {
			listed := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
			current := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
			current.Reservations[0].Instances[0].CapacityReservationId = nil
			awsEnv.EC2API.DescribeInstancesBehavior.Output.Reset()
			// MultiOut pops the last output first
			awsEnv.EC2API.DescribeInstancesBehavior.MultiOut.Add(current)
			awsEnv.EC2API.DescribeInstancesBehavior.MultiOut.Add(listed)
			ExpectApplied(ctx, env.Client, nodeClaim, node)
			ExpectSingletonReconciled(ctx, controller)
			Expect(ExpectExists(ctx, env.Client, nodeClaim).Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeOnDemand))
		})
	})
	It("should demote nodes from reserved to on-demand even if their nodeclaim was demoted previously", func() {
		out := awsEnv.EC2API.DescribeInstancesBehavior.Output.Clone()
		out.Reservations[0].Instances[0].CapacityReservationId = nil
		awsEnv.EC2API.DescribeInstancesBehavior.Output.Set(out)

		ExpectApplied(ctx, env.Client, nodeClaim)
		ExpectSingletonReconciled(ctx, controller)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeOnDemand))
		Expect(nodeClaim.Labels).ToNot(HaveKey(corecloudprovider.ReservationIDLabel))
		Expect(nodeClaim.Labels).ToNot(HaveKey(v1.LabelCapacityReservationType))

		ExpectApplied(ctx, env.Client, node)
		ExpectSingletonReconciled(ctx, controller)
		node = ExpectExists(ctx, env.Client, node)
		Expect(node.Labels).To(HaveKeyWithValue(karpv1.CapacityTypeLabelKey, karpv1.CapacityTypeOnDemand))
		Expect(node.Labels).ToNot(HaveKey(corecloudprovider.ReservationIDLabel))
		Expect(node.Labels).ToNot(HaveKey(v1.LabelCapacityReservationType))
	})
})
