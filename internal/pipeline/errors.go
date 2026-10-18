package pipeline

import (
	"errors"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
)

// IsScheduleToStartTimeout reports whether err is an activity that waited for a worker and none
// picked it up. Callers that are waiting on a person's behalf use it to say "no worker is running"
// instead of surfacing a Temporal error.
func IsScheduleToStartTimeout(err error) bool {
	var te *temporal.TimeoutError
	return errors.As(err, &te) && te.TimeoutType() == enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START
}
