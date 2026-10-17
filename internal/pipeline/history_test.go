//go:build integration

package pipeline

import (
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
)

// historyFrom drains a history iterator into a History for replay.
func historyFrom(it client.HistoryEventIterator) (*historypb.History, error) {
	h := &historypb.History{}
	for it.HasNext() {
		e, err := it.Next()
		if err != nil {
			return nil, err
		}
		h.Events = append(h.Events, e)
	}
	return h, nil
}
