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

package errors_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	awserrors "github.com/aws/karpenter-provider-aws/pkg/errors"
)

// ec2Error returns an error shaped like the ones the EC2 client returns: the API error, which doesn't carry a fault,
// wrapped in the HTTP response it came with and the operation.
func ec2Error(status int, code string) error {
	return fmt.Errorf("wrapped, %w", &smithy.OperationError{
		ServiceID:     "EC2",
		OperationName: "ModifyInstanceCapacityReservationAttributes",
		Err: &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
				Err:      &smithy.GenericAPIError{Code: code, Message: "message"},
			},
		},
	})
}

func TestIsAPIClientError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "client error from EC2", err: ec2Error(http.StatusBadRequest, "ReservationCapacityExceeded"), want: true},
		{name: "unauthorized from EC2", err: ec2Error(http.StatusForbidden, "UnauthorizedOperation"), want: true},
		{name: "server error from EC2", err: ec2Error(http.StatusInternalServerError, "InternalError"), want: false},
		{name: "throttled by EC2", err: ec2Error(http.StatusServiceUnavailable, awserrors.RateLimitingErrorCode), want: false},
		{name: "throttling code with a client status", err: ec2Error(http.StatusBadRequest, awserrors.RateLimitingErrorCode), want: false},
		{name: "client fault without a response", err: &smithy.GenericAPIError{Code: "InvalidParameterValue", Fault: smithy.FaultClient}, want: true},
		{name: "unknown fault without a response", err: &smithy.GenericAPIError{Code: "InvalidParameterValue"}, want: false},
		{name: "not an API error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := awserrors.IsAPIClientError(tc.err); got != tc.want {
				t.Errorf("IsAPIClientError() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestIsReservationFullError(t *testing.T) {
	if !awserrors.IsReservationFullError(ec2Error(http.StatusBadRequest, "ReservationCapacityExceeded")) {
		t.Errorf("expected ReservationCapacityExceeded to be a reservation full error")
	}
	if awserrors.IsReservationFullError(ec2Error(http.StatusBadRequest, "InvalidParameterValue")) {
		t.Errorf("expected InvalidParameterValue not to be a reservation full error")
	}
}
