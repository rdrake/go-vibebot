package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/afternet/go-vibebot/internal/store"
)

// TestInMemConcurrentRecordRetrieve pins thread-safety. Leader-side
// synthesis reads a character's Memory from the world goroutine while the
// character goroutine writes to it; without a mutex the race detector
// flags Record/Retrieve as a data race.
func TestInMemConcurrentRecordRetrieve(t *testing.T) {
	m := NewInMem(50)
	ctx := context.Background()

	const writers = 4
	const readers = 4
	const iters = 200

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_ = m.Record(ctx, store.Event{
					Actor: fmt.Sprintf("w%d", w),
					Kind:  "speech",
				})
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, _ = m.Retrieve(ctx, "", 10)
				_ = m.Summary()
			}
		}()
	}
	wg.Wait()
}
