/*
Copyright 2026 The Railgrid Authors.

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

package dispatch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/railgrid/railgrid/pkg/runner"
	"github.com/railgrid/railgrid/pkg/runner/client"
)

type flakyReader struct {
	*fakeRunner
	inspect func(context.Context, string) (runner.Receipt, error)
	events  func(context.Context, string, uint64) (Stream, error)
}

func (f *flakyReader) Inspect(ctx context.Context, id string) (runner.Receipt, error) {
	if f.inspect != nil {
		return f.inspect(ctx, id)
	}
	return f.fakeRunner.Inspect(ctx, id)
}
func (f *flakyReader) Events(ctx context.Context, id string, after uint64) (Stream, error) {
	if f.events != nil {
		return f.events(ctx, id, after)
	}
	return f.fakeRunner.Events(ctx, id, after)
}

func TestFollowRecoversReceiptReadAfterEdge502(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "terminal"}[terminal], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := &fakeRunner{receipt: receipt(runner.PhaseCompleted), events: []runner.Event{event(1, runner.EventProgress, "evidence ready", "")}}
				if terminal {
					base.events = append(base.events, event(2, runner.EventCompleted, "", ""))
				}
				calls := 0
				f := &flakyReader{fakeRunner: base}
				f.inspect = func(ctx context.Context, id string) (runner.Receipt, error) {
					calls++
					if calls <= 2 {
						return runner.Receipt{}, &client.HTTPError{StatusCode: 502, Body: []byte("failed to connect to edge agent")}
					}
					return base.Inspect(ctx, id)
				}
				out, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, nil)
				if err != nil || !out.Completed() || calls != 3 {
					t.Fatalf("outcome=%+v err=%v calls=%d", out, err, calls)
				}
				if !strings.Contains(out.Summary.Text, "evidence ready") || out.Position.AttemptID != "attempt-1" {
					t.Fatalf("lost attempt/output: %+v", out)
				}
				if len(base.starts)+len(base.resumes)+len(base.cancels) != 0 {
					t.Fatal("read recovery mutated the attempt")
				}
			})
		})
	}
}

type interruptedStream struct {
	sliceStream
	closed bool
}

func (s *interruptedStream) Next(ctx context.Context) (runner.Event, error) {
	if s.i == len(s.events) {
		return runner.Event{}, io.ErrUnexpectedEOF
	}
	return s.sliceStream.Next(ctx)
}
func (s *interruptedStream) Close() error { s.closed = true; return nil }

func TestFollowReconnectsAtConsumedCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &fakeRunner{receipt: receipt(runner.PhaseCompleted), events: []runner.Event{
			event(1, runner.EventProgress, "first", ""), event(2, runner.EventProgress, "second", ""), event(3, runner.EventCompleted, "", "")}}
		broken := &interruptedStream{sliceStream: sliceStream{events: base.events[:1]}}
		calls := 0
		f := &flakyReader{fakeRunner: base}
		f.events = func(ctx context.Context, id string, after uint64) (Stream, error) {
			calls++
			switch calls {
			case 1:
				return nil, &client.HTTPError{StatusCode: 503}
			case 2:
				return broken, nil
			case 3:
				if after != 1 {
					t.Fatalf("reconnected cursor=%d, want 1", after)
				}
			}
			return base.Events(ctx, id, after)
		}
		obs := &recorder{}
		out, err := Follow(ctxWithTimeout(t), f, receipt(runner.PhaseRunning), Position{}, obs)
		if err != nil || !out.Completed() || !broken.closed {
			t.Fatalf("out=%+v err=%v closed=%v", out, err, broken.closed)
		}
		if strings.Count(out.Summary.Text, "first") != 1 || strings.Count(out.Summary.Text, "second") != 1 {
			t.Fatalf("duplicated/lost output %q", out.Summary.Text)
		}
		if len(obs.checkpoints) == 0 || obs.checkpoints[0].Position.Cursor != 1 {
			t.Fatal("missing recovery checkpoint")
		}
	})
}

func TestReadRetryHonorsDeadlineAndDurableCancellation(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "durable"}[durable], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				obs := &recorder{}
				stop := errors.New("user cancelled")
				calls := 0
				_, err := retryRead(ctx, obs, func() (runner.Receipt, error) {
					calls++
					if durable && calls == 2 {
						obs.abort = stop
					}
					return runner.Receipt{}, &client.HTTPError{StatusCode: 502}
				})
				if durable && !errors.Is(err, stop) {
					t.Fatalf("err=%v", err)
				}
				if !durable && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("err=%v", err)
				}
				if calls > 7 {
					t.Fatalf("backoff did not limit calls: %d", calls)
				}
			})
		})
	}
}

func TestReadRetryPreservesPermanentErrors(t *testing.T) {
	for _, err := range []error{&client.HTTPError{StatusCode: 500}, &client.HTTPError{StatusCode: 429}, &client.HTTPError{StatusCode: 401}, &client.HTTPError{StatusCode: 403}, &client.HTTPError{StatusCode: 404},
		&runner.Error{Code: runner.ErrorUnavailable}, &runner.Error{Code: runner.ErrorCursorExpired, Retryable: true, SnapshotRequired: true}, errors.New("invalid event")} {
		calls := 0
		_, got := retryRead(context.Background(), nil, func() (runner.Receipt, error) { calls++; return runner.Receipt{}, err })
		if got != err || calls != 1 {
			t.Fatalf("error=%v got=%v calls=%d", err, got, calls)
		}
	}
}

func TestRetryableReadTransportErrors(t *testing.T) {
	for _, err := range []error{io.ErrUnexpectedEOF, &net.OpError{Op: "read", Err: syscall.ECONNRESET},
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, &client.HTTPError{StatusCode: http.StatusGatewayTimeout},
		&runner.Error{Code: runner.ErrorUnavailable, Retryable: true}} {
		if !retryableRead(err) {
			t.Errorf("not retryable: %v", err)
		}
	}
	if retryableRead(context.Canceled) {
		t.Fatal("retrying cancellation")
	}
}

func TestFollowCancellationDuringOutagePreservesCheckpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &fakeRunner{receipt: receipt(runner.PhaseRunning), events: []runner.Event{event(1, runner.EventProgress, "work in progress", "")}}
		obs := &recorder{}
		stopped := errors.New("durable cancellation requested")
		f := &flakyReader{fakeRunner: base}
		f.inspect = func(context.Context, string) (runner.Receipt, error) {
			obs.abort = stopped
			return runner.Receipt{}, &client.HTTPError{StatusCode: 502}
		}
		out, err := Follow(context.Background(), f, receipt(runner.PhaseRunning), Position{}, obs)
		if !errors.Is(err, stopped) || out.Receipt.Phase != runner.PhaseRunning || out.Snapshot.Position.Cursor != 1 {
			t.Fatalf("lost active attempt/checkpoint on cancel: %+v, %v", out, err)
		}
		if len(base.starts)+len(base.resumes)+len(base.cancels) != 0 {
			t.Fatal("read retry mutated attempt")
		}
	})
}

func TestRejoinRetriesInspectionWithoutRedispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &fakeRunner{receipt: receipt(runner.PhaseCompleted)}
		f := &flakyReader{fakeRunner: base}
		calls := 0
		f.inspect = func(ctx context.Context, id string) (runner.Receipt, error) {
			calls++
			if calls == 1 {
				return runner.Receipt{}, &client.HTTPError{StatusCode: 502}
			}
			return base.Inspect(ctx, id)
		}
		out, err := Rejoin(ctxWithTimeout(t), f, Position{AttemptID: "attempt-1"}, nil)
		if err != nil || !out.Completed() || calls != 2 || len(base.starts)+len(base.resumes) != 0 {
			t.Fatalf("rejoin failed or redispatched: %+v %v calls=%d", out, err, calls)
		}
	})
}

// Persistent failures are bounded even for callers without a deadline. Opening
// a stream successfully between failed reads must not renew the outage budget.
func TestPersistentReadOutage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "inspection", true: "stream"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				failure := &client.HTTPError{StatusCode: 502}
				obs := &recorder{}
				f := &flakyReader{fakeRunner: &fakeRunner{receipt: receipt(runner.PhaseRunning)}}
				f.inspect = func(context.Context, string) (runner.Receipt, error) { return runner.Receipt{}, failure }
				calls := 0
				f.events = func(context.Context, string, uint64) (Stream, error) {
					calls++
					// Exercise alternating connection failures and failed stream reads.
					if calls%2 == 0 {
						return nil, failure
					}
					return &sliceStream{err: failure}, nil
				}
				var err error
				if stream {
					_, err = Follow(context.Background(), f, receipt(runner.PhaseRunning), Position{}, obs)
					if len(obs.checkpoints) != 1 {
						t.Fatalf("redundant checkpoints: %d", len(obs.checkpoints))
					}
				} else {
					_, err = inspectWithRetry(context.Background(), f, "attempt-1", nil)
				}
				if !errors.Is(err, failure) || !strings.Contains(err.Error(), "outage exceeded") {
					t.Fatalf("lost last error or outage diagnostic: %v", err)
				}
				if elapsed := time.Since(start); elapsed != maxReadOutage {
					t.Fatalf("outage lasted %s", elapsed)
				}
			})
		})
	}
}

func TestCancelRetriesObservationOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &fakeRunner{receipt: receipt(runner.PhaseRunning)}
		f := &flakyReader{fakeRunner: base}
		calls := 0
		f.inspect = func(context.Context, string) (runner.Receipt, error) {
			calls++
			if calls == 1 {
				return runner.Receipt{}, &client.HTTPError{StatusCode: 502}
			}
			return receipt(runner.PhaseCancelled), nil
		}
		got, err := Cancel(context.Background(), f, runner.CancelRequest{AttemptID: "attempt-1"})
		if err != nil || got == nil || got.Phase != runner.PhaseCancelled || calls != 2 || len(base.cancels) != 1 {
			t.Fatalf("receipt=%+v err=%v reads=%d mutations=%d", got, err, calls, len(base.cancels))
		}
	})
}

type countingCancellation struct {
	Noop
	checks int
	stopAt time.Time
}

func (o *countingCancellation) Aborted(context.Context) error {
	o.checks++
	if !time.Now().Before(o.stopAt) {
		return context.Canceled
	}
	return nil
}

func TestBackoffCancellationPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		obs := &countingCancellation{stopAt: start.Add(3500 * time.Millisecond)}
		delay := 5 * time.Second
		err := waitReadRetry(context.Background(), obs, &delay)
		if !errors.Is(err, context.Canceled) || time.Since(start) > 4500*time.Millisecond || obs.checks > 5 {
			t.Fatalf("err=%v elapsed=%s durable reads=%d", err, time.Since(start), obs.checks)
		}
	})
}

func TestRestoreSnapshotReconnectsSilently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := &fakeRunner{events: []runner.Event{
			event(1, runner.EventProgress, "first", ""),
			event(2, runner.EventProgress, "second", ""),
		}}
		broken := &interruptedStream{sliceStream: sliceStream{events: base.events[:1]}}
		f := &flakyReader{fakeRunner: base}
		calls := 0
		f.events = func(ctx context.Context, id string, after uint64) (Stream, error) {
			calls++
			if calls == 1 {
				return broken, nil
			}
			if after != 1 {
				t.Fatalf("replay cursor=%d", after)
			}
			if calls == 2 {
				return nil, &client.HTTPError{StatusCode: 502}
			}
			return base.Events(ctx, id, after)
		}
		obs := &recorder{}
		snap, err := RestoreSnapshot(context.Background(), f, Position{AttemptID: "attempt-1", Cursor: 2}, obs)
		if err != nil || snap.Position.Cursor != 2 || !broken.closed || calls != 3 {
			t.Fatalf("snapshot=%+v err=%v closed=%v calls=%d", snap, err, broken.closed, calls)
		}
		restored, err := restoreStream(nil, snap)
		if err != nil {
			t.Fatal(err)
		}
		text := restored.summary().Text
		if strings.Count(text, "first") != 1 || strings.Count(text, "second") != 1 {
			t.Fatalf("lost or duplicated replay: %q", text)
		}
		if len(obs.text) != 0 || len(obs.checkpoints) != 0 {
			t.Fatal("replay emitted output")
		}
	})
}

func TestRestoreSnapshotPersistentDisconnectIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		f := &flakyReader{fakeRunner: &fakeRunner{}}
		failure := &client.HTTPError{StatusCode: 502}
		f.events = func(context.Context, string, uint64) (Stream, error) {
			return &sliceStream{err: failure}, nil
		}
		_, err := RestoreSnapshot(context.Background(), f, Position{AttemptID: "attempt-1", Cursor: 1}, nil)
		if !errors.Is(err, failure) || time.Since(start) != maxReadOutage {
			t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
		}
	})
}

func TestCancelPreservesReceiptOnReadFailure(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "outage", true: "deadline"}[deadline], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := &fakeRunner{receipt: receipt(runner.PhaseCancelling)}
				f := &flakyReader{fakeRunner: base}
				failure := &client.HTTPError{StatusCode: 502}
				f.inspect = func(context.Context, string) (runner.Receipt, error) { return runner.Receipt{}, failure }
				ctx := context.Background()
				if deadline {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
				}
				got, err := Cancel(ctx, f, runner.CancelRequest{AttemptID: "attempt-1"})
				if got == nil || got.Phase != runner.PhaseCancelling || err == nil ||
					!strings.Contains(err.Error(), "not cancelled") || !strings.Contains(err.Error(), "502") || len(base.cancels) != 1 {
					t.Fatalf("receipt=%+v err=%v mutations=%d", got, err, len(base.cancels))
				}
				if deadline && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost deadline: %v", err)
				}
				if !deadline && !errors.Is(err, failure) {
					t.Fatalf("lost outage failure: %v", err)
				}
			})
		})
	}
}
