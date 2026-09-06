package flowcontrol

import "context"

// HoldChannel retains a slot until the producer has terminated, not merely until
// the function returns or the first chunk arrives. On cancellation it drains the
// producer (which receives the same canceled context) before releasing. A broken
// producer that ignores cancellation stays accounted rather than permitting
// unbounded overlapping upstream work through early local release.
func HoldChannel[T any](ctx context.Context, source <-chan T, release func(), onCancel ...func()) <-chan T {
	if ctx == nil {
		ctx = context.Background()
	}
	cancelled := func() {
		for _, fn := range onCancel {
			if fn != nil {
				fn()
			}
		}
	}
	out := make(chan T)
	go func() {
		defer close(out)
		defer release()
		for {
			select {
			case <-ctx.Done():
				cancelled()
				for range source {
				}
				return
			case value, ok := <-source:
				if !ok {
					return
				}
				select {
				case out <- value:
				case <-ctx.Done():
					cancelled()
					for range source {
					}
					return
				}
			}
		}
	}()
	return out
}
