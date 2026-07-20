/*
Copyright 2026 The Faros Authors.

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

package events

import (
	"context"
	"testing"
	"time"
)

func ev(id, typ, cam string, t time.Time) Event {
	return Event{ID: id, Type: typ, CameraID: cam, Start: t}
}

func TestMemoryStore_RingCapEvictsOldest(t *testing.T) {
	s := NewMemoryStore(3, 0)
	k := Key{Cluster: "c1", Service: "cam"}
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5; i++ {
		if err := s.Append(context.Background(), k, ev(string(rune('a'+i)), "motion", "x", base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(context.Background(), k, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 retained (cap), got %d", len(got))
	}
	// Newest first; the two oldest (a,b) were evicted, leaving e,d,c.
	if got[0].ID != "e" || got[2].ID != "c" {
		t.Fatalf("unexpected retained order: %v", ids(got))
	}
}

func TestMemoryStore_IsolationByKey(t *testing.T) {
	s := NewMemoryStore(10, 0)
	now := time.Unix(1_700_000_000, 0)
	a := Key{Cluster: "tenantA", Service: "cam"}
	b := Key{Cluster: "tenantB", Service: "cam"} // same service name, different tenant
	_ = s.Append(context.Background(), a, ev("a1", "motion", "x", now))
	_ = s.Append(context.Background(), b, ev("b1", "ring", "y", now))

	ga, _ := s.List(context.Background(), a, Filter{})
	gb, _ := s.List(context.Background(), b, Filter{})
	if len(ga) != 1 || ga[0].ID != "a1" {
		t.Fatalf("tenantA leak/miss: %v", ids(ga))
	}
	if len(gb) != 1 || gb[0].ID != "b1" {
		t.Fatalf("tenantB leak/miss: %v", ids(gb))
	}
}

func TestMemoryStore_Filter(t *testing.T) {
	s := NewMemoryStore(50, 0)
	k := Key{Cluster: "c", Service: "cam"}
	now := time.Unix(1_700_000_000, 0)
	_ = s.Append(context.Background(), k, ev("old", "motion", "cam1", now.Add(-2*time.Hour)))
	_ = s.Append(context.Background(), k, ev("m1", "motion", "cam1", now.Add(-10*time.Minute)))
	_ = s.Append(context.Background(), k, ev("r1", "ring", "cam2", now.Add(-5*time.Minute)))

	// Since drops the 2h-old event.
	got, _ := s.List(context.Background(), k, Filter{Since: now.Add(-30 * time.Minute)})
	if len(got) != 2 {
		t.Fatalf("since: want 2, got %v", ids(got))
	}
	// Type filter.
	got, _ = s.List(context.Background(), k, Filter{Types: []string{"ring"}})
	if len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("types: got %v", ids(got))
	}
	// Camera filter.
	got, _ = s.List(context.Background(), k, Filter{CameraID: "cam1"})
	if len(got) != 2 {
		t.Fatalf("camera: want 2, got %v", ids(got))
	}
	// Limit, newest first.
	got, _ = s.List(context.Background(), k, Filter{Limit: 1})
	if len(got) != 1 || got[0].ID != "r1" {
		t.Fatalf("limit: got %v", ids(got))
	}
}

func TestMemoryStore_MaxAgeOnRead(t *testing.T) {
	s := NewMemoryStore(50, time.Hour)
	fixed := time.Unix(1_700_000_000, 0)
	s.nowFunc = func() time.Time { return fixed }
	k := Key{Cluster: "c", Service: "cam"}
	_ = s.Append(context.Background(), k, ev("stale", "motion", "x", fixed.Add(-2*time.Hour)))
	_ = s.Append(context.Background(), k, ev("fresh", "motion", "x", fixed.Add(-10*time.Minute)))
	got, _ := s.List(context.Background(), k, Filter{})
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("maxAge: got %v", ids(got))
	}
}

func TestMemoryStore_Clear(t *testing.T) {
	s := NewMemoryStore(10, 0)
	k := Key{Cluster: "c", Service: "cam"}
	_ = s.Append(context.Background(), k, ev("a", "motion", "x", time.Unix(1, 0)))
	_ = s.Clear(context.Background(), k)
	got, _ := s.List(context.Background(), k, Filter{})
	if len(got) != 0 {
		t.Fatalf("clear: want 0, got %v", ids(got))
	}
}

func ids(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}
