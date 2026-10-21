package main

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	dnspod "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/dnspod/v20210323"
)

const (
	ttA = "10.0.0.1"
	ttB = "10.0.0.2"
	ttC = "10.0.0.3"
	ttX = "10.0.9.9" // never configured
)

var ttS = recordShape{Line: "默认", TTL: 60, Remark: "hc"}

// ttDesiredFields lists the Desired fields compared for every expected change.
var ttDesiredFields = []string{"RecordId", "Type", "Value", "Line", "LineId", "TTL", "Remark", "Status"}

type ttChange struct {
	id      uint64
	reason  changeReason
	desired *dnspod.RecordListItem
	diffs   []string // when set, Diffs must equal it
	has     []string // Diffs must contain these, in this order
}

type ttCase struct {
	name       string
	records    []*dnspod.RecordListItem
	configured []string // nil means ttA, ttB
	alive      []string
	creates    []string
	changes    []ttChange
}

func TestReconcile(t *testing.T) {
	for _, tc := range []ttCase{
		{
			name:    "P1 empty remote creates every alive IP",
			alive:   []string{ttA, ttB},
			creates: []string{ttA, ttB},
		},
		{
			name:    "P2 alive IP without a record is created",
			records: []*dnspod.RecordListItem{ttShaped(1, ttA, "ENABLE")},
			alive:   []string{ttA, ttB},
			creates: []string{ttB},
		},
		{
			name:    "P3 alive record in shape needs nothing",
			records: []*dnspod.RecordListItem{ttShaped(1, ttA, "ENABLE")},
			alive:   []string{ttA},
		},
		{
			name:    "P4 disabled alive record is enabled",
			records: []*dnspod.RecordListItem{ttShaped(1, ttA, "DISABLE")},
			alive:   []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE"), diffs: []string{"Status: DISABLE -> ENABLE"}},
			},
		},
		{
			name:    "P4 alive record TTL is normalized",
			records: []*dnspod.RecordListItem{ttRec(1, ttA, "默认", 600, "hc", "DISABLE")},
			alive:   []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE"), has: []string{"TTL: 600 -> 60"}},
			},
		},
		{
			name:    "P4 alive record moves to the shape line without its LineId",
			records: []*dnspod.RecordListItem{ttLineID(ttRec(1, ttA, "电信", 60, "hc", "DISABLE"), "10=0")},
			alive:   []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE"), has: []string{"Line: 电信 -> 默认"}},
			},
		},
		{
			name:    "P4 alive record remark is normalized",
			records: []*dnspod.RecordListItem{ttRec(1, ttA, "默认", 60, "old", "DISABLE")},
			alive:   []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE"), has: []string{"Remark: old -> hc"}},
			},
		},
		{
			name:    "P5 dead record is disabled",
			records: []*dnspod.RecordListItem{ttShaped(1, ttB, "ENABLE")},
			alive:   []string{ttA},
			creates: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonDead, desired: ttShaped(1, ttB, "DISABLE")},
			},
		},
		{
			name:    "P5 dead record already disabled needs nothing",
			records: []*dnspod.RecordListItem{ttShaped(1, ttB, "DISABLE")},
			alive:   []string{ttA},
			creates: []string{ttA},
		},
		{
			name:    "P6 unmanaged record is disabled and normalized",
			records: []*dnspod.RecordListItem{ttRec(1, ttX, "电信", 600, "", "ENABLE")},
			alive:   []string{ttA},
			creates: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonUnmanaged, desired: ttShaped(1, ttX, "DISABLE"),
					diffs: []string{"Line: 电信 -> 默认", "TTL: 600 -> 60", "Status: ENABLE -> DISABLE", "Remark:  -> hc"}},
			},
		},
		{
			name:    "P7 unmanaged record disabled in shape needs nothing",
			records: []*dnspod.RecordListItem{ttShaped(1, ttX, "DISABLE")},
			alive:   []string{ttA},
			creates: []string{ttA},
		},
		{
			name: "P8 duplicate beside the shape-line record is disabled as is",
			records: []*dnspod.RecordListItem{
				ttLineID(ttRec(1, ttA, "电信", 60, "hc", "ENABLE"), "10=0"),
				ttShaped(2, ttA, "ENABLE"),
			},
			alive: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonDuplicate, desired: ttLineID(ttRec(1, ttA, "电信", 60, "hc", "DISABLE"), "10=0"),
					diffs: []string{"Status: ENABLE -> DISABLE"}},
			},
		},
		{
			name: "P8 duplicate already disabled needs nothing",
			records: []*dnspod.RecordListItem{
				ttLineID(ttRec(1, ttA, "电信", 60, "hc", "DISABLE"), "10=0"),
				ttShaped(2, ttA, "ENABLE"),
			},
			alive: []string{ttA},
		},
		{
			name: "P8 nil-line record is a duplicate keeping its nil fields",
			records: []*dnspod.RecordListItem{
				{RecordId: new(uint64(1)), Type: new("A"), Value: new(ttA), Status: new("ENABLE")},
				ttShaped(2, ttA, "ENABLE"),
			},
			alive: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonDuplicate,
					desired: &dnspod.RecordListItem{RecordId: new(uint64(1)), Type: new("A"), Value: new(ttA), Status: new("DISABLE")},
					diffs:   []string{"Status: ENABLE -> DISABLE"}},
			},
		},
		{
			name: "P9 first record is primary when none is on the shape line",
			records: []*dnspod.RecordListItem{
				ttRec(1, ttA, "电信", 60, "hc", "ENABLE"),
				ttRec(2, ttA, "联通", 60, "hc", "ENABLE"),
			},
			alive: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE"), has: []string{"Line: 电信 -> 默认"}},
				{id: 2, reason: reasonDuplicate, desired: ttRec(2, ttA, "联通", 60, "hc", "DISABLE")},
			},
		},
		{
			name: "P9 unmanaged value has an unmanaged primary and a duplicate",
			records: []*dnspod.RecordListItem{
				ttRec(1, ttX, "电信", 60, "hc", "ENABLE"),
				ttRec(2, ttX, "联通", 60, "hc", "ENABLE"),
			},
			alive:   []string{ttA},
			creates: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonUnmanaged, desired: ttShaped(1, ttX, "DISABLE")},
				{id: 2, reason: reasonDuplicate, desired: ttRec(2, ttX, "联通", 60, "hc", "DISABLE")},
			},
		},
		{
			name: "P9 dead value has a dead primary and a duplicate",
			records: []*dnspod.RecordListItem{
				ttRec(1, ttB, "电信", 60, "hc", "ENABLE"),
				ttRec(2, ttB, "联通", 60, "hc", "ENABLE"),
			},
			alive:   []string{ttA},
			creates: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonDead, desired: ttShaped(1, ttB, "DISABLE")},
				{id: 2, reason: reasonDuplicate, desired: ttRec(2, ttB, "联通", 60, "hc", "DISABLE")},
			},
		},
		{
			name: "P9 first shape-line record is primary over a later one",
			records: []*dnspod.RecordListItem{
				ttShaped(1, ttA, "ENABLE"),
				ttShaped(2, ttA, "ENABLE"),
			},
			alive: []string{ttA},
			changes: []ttChange{
				{id: 2, reason: reasonDuplicate, desired: ttShaped(2, ttA, "DISABLE"),
					diffs: []string{"Status: ENABLE -> DISABLE"}},
			},
		},
		{
			name: "P10 records of other types are ignored",
			records: []*dnspod.RecordListItem{
				ttWith(ttShaped(1, ttA, "ENABLE"), func(r *dnspod.RecordListItem) { r.Type = new("CNAME") }),
				ttWith(ttShaped(2, "mail.example.com.", "ENABLE"), func(r *dnspod.RecordListItem) { r.Type = new("MX") }),
			},
			alive:   []string{ttA},
			creates: []string{ttA},
		},
		{
			name:    "P11 type-nil record is handled as an A record",
			records: []*dnspod.RecordListItem{ttWith(ttShaped(1, ttA, "DISABLE"), func(r *dnspod.RecordListItem) { r.Type = nil })},
			alive:   []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE"), diffs: []string{"Status: DISABLE -> ENABLE"}},
			},
		},
		{
			name:    "P11 value-nil record is ignored",
			records: []*dnspod.RecordListItem{ttWith(ttShaped(1, ttX, "ENABLE"), func(r *dnspod.RecordListItem) { r.Value = nil })},
			alive:   []string{ttA},
			creates: []string{ttA},
		},
		{
			name:    "P11 recordid-nil record is ignored",
			records: []*dnspod.RecordListItem{ttWith(ttShaped(1, ttA, "DISABLE"), func(r *dnspod.RecordListItem) { r.RecordId = nil })},
			alive:   []string{ttA},
			creates: []string{ttA},
		},
		{
			name:       "P12 nothing alive leaves configured dead and others unmanaged",
			records:    []*dnspod.RecordListItem{ttShaped(1, ttA, "ENABLE"), ttShaped(2, ttX, "ENABLE")},
			configured: []string{ttA},
			alive:      []string{},
			changes: []ttChange{
				{id: 1, reason: reasonDead, desired: ttShaped(1, ttA, "DISABLE")},
				{id: 2, reason: reasonUnmanaged, desired: ttShaped(2, ttX, "DISABLE")},
			},
		},
		{
			name:       "P13 creates follow alive order and changes follow remote order",
			records:    []*dnspod.RecordListItem{ttShaped(1, ttX, "ENABLE"), ttShaped(2, ttB, "DISABLE")},
			configured: []string{ttC, ttA, ttB},
			alive:      []string{ttC, ttA, ttB},
			creates:    []string{ttC, ttA},
			changes: []ttChange{
				{id: 1, reason: reasonUnmanaged, desired: ttShaped(1, ttX, "DISABLE")},
				{id: 2, reason: reasonAlive, desired: ttShaped(2, ttB, "ENABLE")},
			},
		},
		{
			name: "P13 changes follow remote order across interleaved values",
			records: []*dnspod.RecordListItem{
				ttRec(1, ttA, "电信", 60, "hc", "ENABLE"),
				ttShaped(2, ttX, "ENABLE"),
				ttRec(3, ttA, "联通", 60, "hc", "ENABLE"),
				ttShaped(4, ttB, "ENABLE"),
			},
			alive: []string{ttA},
			changes: []ttChange{
				{id: 1, reason: reasonAlive, desired: ttShaped(1, ttA, "ENABLE")},
				{id: 2, reason: reasonUnmanaged, desired: ttShaped(2, ttX, "DISABLE")},
				{id: 3, reason: reasonDuplicate, desired: ttRec(3, ttA, "联通", 60, "hc", "DISABLE")},
				{id: 4, reason: reasonDead, desired: ttShaped(4, ttB, "DISABLE")},
			},
		},
		{
			name:    "P14 status-nil record otherwise in shape needs nothing",
			records: []*dnspod.RecordListItem{ttWith(ttShaped(1, ttX, ""), func(r *dnspod.RecordListItem) { r.Status = nil })},
			alive:   []string{ttA},
			creates: []string{ttA},
		},
		{
			name:       "P15 repeated alive IP is created once",
			configured: []string{ttA, ttA},
			alive:      []string{ttA, ttA},
			creates:    []string{ttA},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configured := tc.configured
			if configured == nil {
				configured = []string{ttA, ttB}
			}
			got := ttPlan(t, tc.records, configured, tc.alive)

			if !slices.Equal(got.Creates, tc.creates) {
				t.Errorf("Creates = %q, want %q", got.Creates, tc.creates)
			}
			var wantIDs []string
			for _, w := range tc.changes {
				wantIDs = append(wantIDs, strconv.FormatUint(w.id, 10))
			}
			if gotIDs := ttIDs(got.Changes); !slices.Equal(gotIDs, wantIDs) {
				t.Errorf("Changes are for records %v, want %v", gotIDs, wantIDs)
			}
			for i, w := range tc.changes {
				j := slices.IndexFunc(got.Changes, func(c recordChange) bool { return ttID(c.Record) == wantIDs[i] })
				if j < 0 {
					continue
				}
				c := got.Changes[j]
				if c.Reason != w.reason {
					t.Errorf("record %d: Reason = %s, want %s", w.id, ttReason(c.Reason), ttReason(w.reason))
				}
				if c.Desired == nil {
					t.Errorf("record %d: Desired is nil", w.id)
				} else {
					for _, d := range ttFieldDiffs(c.Desired, w.desired, ttDesiredFields...) {
						t.Errorf("record %d: Desired.%s", w.id, d)
					}
				}
				if len(c.Diffs) == 0 {
					t.Errorf("record %d: change planned with empty Diffs", w.id)
				}
				if w.diffs != nil && !slices.Equal(c.Diffs, w.diffs) {
					t.Errorf("record %d: Diffs = %q, want %q", w.id, c.Diffs, w.diffs)
				}
				rest := w.has
				for _, d := range c.Diffs {
					if len(rest) > 0 && d == rest[0] {
						rest = rest[1:]
					}
				}
				if len(rest) > 0 {
					t.Errorf("record %d: Diffs = %q, want them to contain %q in that order", w.id, c.Diffs, w.has)
				}
			}
		})
	}
}

func TestReconcileIsPure(t *testing.T) {
	t.Run("P16 same input gives the same plan", func(t *testing.T) {
		records, configured, alive := ttPureInput()
		first := ttPlan(t, records, configured, alive)
		ttNonVacuous(t, first)
		for i := range 20 {
			if again := ttPlan(t, records, configured, alive); !reflect.DeepEqual(again, first) {
				t.Fatalf("call %d returned a different plan (Creates %q, Changes for %v) than the first (Creates %q, Changes for %v)",
					i+2, again.Creates, ttIDs(again.Changes), first.Creates, ttIDs(first.Changes))
			}
		}
	})
	t.Run("P16 inputs are not mutated", func(t *testing.T) {
		records, configured, alive := ttPureInput()
		inputs := slices.Clone(records)
		ttNonVacuous(t, ttPlan(t, records, configured, alive))
		wantRecords, wantConfigured, wantAlive := ttPureInput()
		if !slices.Equal(records, inputs) {
			t.Error("records slice elements were replaced or reordered")
		}
		for i, r := range inputs {
			if d := ttFieldDiffs(r, wantRecords[i]); len(d) > 0 {
				t.Errorf("input record %s was mutated: %s", ttID(wantRecords[i]), strings.Join(d, "; "))
			}
		}
		if !slices.Equal(configured, wantConfigured) {
			t.Errorf("configured = %q after reconcile, want %q", configured, wantConfigured)
		}
		if !slices.Equal(alive, wantAlive) {
			t.Errorf("alive = %q after reconcile, want %q", alive, wantAlive)
		}
	})
	t.Run("P16 each Desired is a new record", func(t *testing.T) {
		records, configured, alive := ttPureInput()
		plan := ttPlan(t, records, configured, alive)
		ttNonVacuous(t, plan)
		for _, c := range plan.Changes {
			if c.Desired == nil || slices.Contains(records, c.Desired) {
				t.Errorf("record %s: Desired is nil or an input record, want a new allocation", ttID(c.Record))
			}
		}
	})
	t.Run("P16 each Record is the input record", func(t *testing.T) {
		records, configured, alive := ttPureInput()
		plan := ttPlan(t, records, configured, alive)
		ttNonVacuous(t, plan)
		for _, c := range plan.Changes {
			if !slices.Contains(records, c.Record) {
				t.Errorf("record %s: Record is not the input pointer", ttID(c.Record))
			}
		}
	})
}

// ttPureInput needs two creates, two primary changes and two duplicate changes.
func ttPureInput() (records []*dnspod.RecordListItem, configured, alive []string) {
	return []*dnspod.RecordListItem{
		ttLineID(ttRec(1, ttX, "电信", 600, "", "ENABLE"), "10=0"),
		ttShaped(2, ttA, "DISABLE"),
		ttShaped(3, ttX, "ENABLE"),
		ttLineID(ttRec(4, ttA, "联通", 60, "hc", "ENABLE"), "10=1"),
	}, []string{ttA, ttB, ttC}, []string{ttC, ttB, ttA}
}

func ttNonVacuous(t *testing.T, plan reconcilePlan) {
	t.Helper()
	if len(plan.Changes) == 0 {
		t.Fatal("reconcile planned no changes for an input needing four; nothing to check")
	}
}

// ttPlan runs reconcile with shape ttS, reporting a panic as a failure.
func ttPlan(t *testing.T, records []*dnspod.RecordListItem, configured, alive []string) reconcilePlan {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("reconcile panicked: %v", p)
		}
	}()
	return reconcile(records, configured, alive, ttS)
}

func ttRec(id uint64, value, line string, ttl uint64, remark, status string) *dnspod.RecordListItem {
	return &dnspod.RecordListItem{
		RecordId: new(id),
		Type:     new("A"),
		Value:    new(value),
		Line:     new(line),
		TTL:      new(ttl),
		Remark:   new(remark),
		Status:   new(status),
	}
}

// ttShaped builds an A record whose Line, TTL and Remark already match ttS.
func ttShaped(id uint64, value, status string) *dnspod.RecordListItem {
	return ttRec(id, value, ttS.Line, ttS.TTL, ttS.Remark, status)
}

func ttLineID(r *dnspod.RecordListItem, lineID string) *dnspod.RecordListItem {
	r.LineId = new(lineID)
	return r
}

func ttWith(r *dnspod.RecordListItem, edit func(*dnspod.RecordListItem)) *dnspod.RecordListItem {
	edit(r)
	return r
}

func ttID(r *dnspod.RecordListItem) string {
	if r == nil || r.RecordId == nil {
		return "nil"
	}
	return strconv.FormatUint(*r.RecordId, 10)
}

func ttIDs(changes []recordChange) []string {
	var ids []string
	for _, c := range changes {
		ids = append(ids, ttID(c.Record))
	}
	return ids
}

func ttReason(r changeReason) string {
	switch r {
	case reasonAlive:
		return "reasonAlive"
	case reasonDead:
		return "reasonDead"
	case reasonUnmanaged:
		return "reasonUnmanaged"
	case reasonDuplicate:
		return "reasonDuplicate"
	}
	return fmt.Sprintf("changeReason(%d)", int(r))
}

// ttFieldDiffs describes the named fields (all when none are named) where got differs from want.
func ttFieldDiffs(got, want *dnspod.RecordListItem, fields ...string) []string {
	g, w := reflect.ValueOf(got).Elem(), reflect.ValueOf(want).Elem()
	if len(fields) == 0 {
		for _, f := range reflect.VisibleFields(g.Type()) {
			fields = append(fields, f.Name)
		}
	}
	show := func(v reflect.Value) string {
		if v.IsNil() {
			return "nil"
		}
		if s, ok := v.Elem().Interface().(string); ok {
			return strconv.Quote(s)
		}
		return fmt.Sprint(v.Elem().Interface())
	}
	var out []string
	for _, name := range fields {
		gf, wf := g.FieldByName(name), w.FieldByName(name)
		if !reflect.DeepEqual(gf.Interface(), wf.Interface()) {
			out = append(out, fmt.Sprintf("%s = %s, want %s", name, show(gf), show(wf)))
		}
	}
	return out
}
